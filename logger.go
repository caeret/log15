package log15

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/go-stack/stack"
)

const timeKey = "t"
const lvlKey = "lvl"
const msgKey = "msg"
const errorKey = "LOG15_ERROR"

// Lvl is a type for predefined log levels.
type Lvl int

// List of predefined log Levels
const (
	LvlCrit Lvl = iota
	LvlError
	LvlWarn
	LvlInfo
	LvlDebug
)

// Returns the name of a Lvl
func (l Lvl) String() string {
	switch l {
	case LvlDebug:
		return "dbug"
	case LvlInfo:
		return "info"
	case LvlWarn:
		return "warn"
	case LvlError:
		return "eror"
	case LvlCrit:
		return "crit"
	default:
		panic("bad level")
	}
}

// LvlFromString returns the appropriate Lvl from a string name.
// Useful for parsing command line args and configuration files.
func LvlFromString(lvlString string) (Lvl, error) {
	switch lvlString {
	case "debug", "dbug":
		return LvlDebug, nil
	case "info":
		return LvlInfo, nil
	case "warn":
		return LvlWarn, nil
	case "error", "eror":
		return LvlError, nil
	case "crit":
		return LvlCrit, nil
	default:
		// try to catch e.g. "INFO", "WARN" without slowing down the fast path
		lower := strings.ToLower(lvlString)
		if lower != lvlString {
			return LvlFromString(lower)
		}
		return LvlDebug, fmt.Errorf("log15: unknown level: %v", lvlString)
	}
}

// A Record is what a Logger asks its handler to write
type Record struct {
	Time     time.Time
	Lvl      Lvl
	Msg      string
	Ctx      []interface{}
	Call     stack.Call
	KeyNames RecordKeyNames
}

// RecordKeyNames are the predefined names of the log props used by the Logger interface.
type RecordKeyNames struct {
	Time string
	Msg  string
	Lvl  string
}

// A Logger writes key/value pairs to a Handler
type Logger interface {
	// New returns a new Logger that has this logger's context plus the given context
	New(ctx ...interface{}) Logger

	// GetHandler gets the handler associated with the logger.
	GetHandler() Handler

	// SetHandler updates the logger to write records to the specified handler.
	SetHandler(h Handler)

	// Log a message at the given level with context key/value pairs
	Debug(msg string, ctx ...interface{})
	Info(msg string, ctx ...interface{})
	Warn(msg string, ctx ...interface{})
	Error(msg string, ctx ...interface{})
	Crit(msg string, ctx ...interface{})

	SetTrimedPKG(...string)
}

type logger struct {
	ctx       []interface{}
	h         *swapHandler
	trimedPKG []string
}

func pkgFilePath(frame *runtime.Frame) string {
	pre := pkgPrefix(frame.Function)
	post := pathSuffix(frame.File)
	if pre == "" {
		return post
	}
	return pre + "/" + post
}

// pkgPrefix returns the import path of the function's package with the final
// segment removed.
func pkgPrefix(funcName string) string {
	const pathSep = "/"
	end := strings.LastIndex(funcName, pathSep)
	if end == -1 {
		return ""
	}
	return funcName[:end]
}

// pathSuffix returns the last two segments of path.
func pathSuffix(path string) string {
	const pathSep = "/"
	lastSep := strings.LastIndex(path, pathSep)
	if lastSep == -1 {
		return path
	}
	return path[strings.LastIndex(path[:lastSep], pathSep)+1:]
}

func (l *logger) write(msg string, lvl Lvl, ctx []interface{}) {
	var (
		call stack.Call
		ok   bool
	)
	if len(l.trimedPKG) > 0 {
		found := false
	foo:
		for _, frame := range stack.Trace().TrimRuntime() {
			f := frame.Frame()
			path := pkgFilePath(&f)
			for _, pkg := range l.trimedPKG {
				if strings.HasPrefix(path, pkg) {
					found = true
					continue foo
				}
				if !found {
					continue foo
				}
			}

			if found {
				call = frame
				ok = true
				break
			}
		}
	}
	if !ok {
		call = stack.Caller(2)
	}

	l.h.Log(&Record{
		Time: time.Now(),
		Lvl:  lvl,
		Msg:  msg,
		Ctx:  newContext(l.ctx, ctx),
		Call: call,
		KeyNames: RecordKeyNames{
			Time: timeKey,
			Msg:  msgKey,
			Lvl:  lvlKey,
		},
	})
}

func (l *logger) New(ctx ...interface{}) Logger {
	child := &logger{newContext(l.ctx, ctx), new(swapHandler), nil}
	child.SetHandler(l.h)
	return child
}

func newContext(prefix []interface{}, suffix []interface{}) []interface{} {
	normalizedSuffix := normalize(suffix)
	newCtx := make([]interface{}, len(prefix)+len(normalizedSuffix))
	n := copy(newCtx, prefix)
	copy(newCtx[n:], normalizedSuffix)
	return newCtx
}

func (l *logger) Debug(msg string, ctx ...interface{}) {
	l.write(msg, LvlDebug, ctx)
}

func (l *logger) Info(msg string, ctx ...interface{}) {
	l.write(msg, LvlInfo, ctx)
}

func (l *logger) Warn(msg string, ctx ...interface{}) {
	l.write(msg, LvlWarn, ctx)
}

func (l *logger) Error(msg string, ctx ...interface{}) {
	l.write(msg, LvlError, ctx)
}

func (l *logger) Crit(msg string, ctx ...interface{}) {
	l.write(msg, LvlCrit, ctx)
}

func (l *logger) GetHandler() Handler {
	return l.h.Get()
}

func (l *logger) SetHandler(h Handler) {
	l.h.Swap(h)
}

func (l *logger) SetTrimedPKG(s ...string) {
	l.trimedPKG = s
}

func normalize(ctx []interface{}) []interface{} {
	// if the caller passed a Ctx object, then expand it
	if len(ctx) == 1 {
		if ctxMap, ok := ctx[0].(Ctx); ok {
			ctx = ctxMap.toArray()
		}
	}

	// ctx needs to be even because it's a series of key/value pairs
	// no one wants to check for errors on logging functions,
	// so instead of erroring on bad input, we'll just make sure
	// that things are the right length and users can fix bugs
	// when they see the output looks wrong
	if len(ctx)%2 != 0 {
		ctx = append(ctx, nil, errorKey, "Normalized odd number of arguments by adding nil")
	}

	return ctx
}

// Lazy allows you to defer calculation of a logged value that is expensive
// to compute until it is certain that it must be evaluated with the given filters.
//
// Lazy may also be used in conjunction with a Logger's New() function
// to generate a child logger which always reports the current value of changing
// state.
//
// You may wrap any function which takes no arguments to Lazy. It may return any
// number of values of any type.
type Lazy struct {
	Fn interface{}
}

// Ctx is a map of key/value pairs to pass as context to a log function
// Use this only if you really need greater safety around the arguments you pass
// to the logging functions.
type Ctx map[string]interface{}

func (c Ctx) toArray() []interface{} {
	arr := make([]interface{}, len(c)*2)

	i := 0
	for k, v := range c {
		arr[i] = k
		arr[i+1] = v
		i += 2
	}

	return arr
}
