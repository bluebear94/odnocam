package main

import (
	"syscall/js"
	"unsafe"

	"github.com/domino14/macondo/analyzer"
	"github.com/domino14/macondo/cache"
)

func precache(this js.Value, args []js.Value) interface{} {
	cache.Precache(args[0].String(), readJSBytes(args[1]))
	return nil
}

func analyze(this js.Value, args []js.Value) (interface{}, error) {
	// JS doesn't use utf8, but it converts automatically if we take/return strings.
	jsonBoardStr := args[0].String()
	jsonBoard := *(*[]byte)(unsafe.Pointer(&jsonBoardStr))

	an := analyzer.NewDefaultAnalyzer()
	jsonMoves, err := an.Analyze(jsonBoard)
	if err != nil {
		return nil, err
	}
	jsonMovesStr := *(*string)(unsafe.Pointer(&jsonMoves))
	return jsonMovesStr, nil
}

func registerCallbacks() {
	js.Global().Get("resMacondo").Invoke(map[string]interface{}{
		"precache": js.FuncOf(precache),
		"analyze":  js.FuncOf(asyncFunc(analyze)),
	})
}

func main() {
	registerCallbacks()
	// Keep Go "program" running.
	select {}
}
