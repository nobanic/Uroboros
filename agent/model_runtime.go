package agent

import (
	"github.com/nobanic/norma/agentcore"
	"github.com/nobanic/norma/llm"
	"github.com/nobanic/norma/transcript"
)

// modelRuntime is value-owned by one actor. Resolvers stay live and each role
// retains its original execution, prompt, tool, clock and history policies.
type modelRuntime struct {
	prov           llm.Provider
	model          string
	workDir        string
	tx             *transcript.Store
	window         int
	proxyAddr      string
	proxyCACert    string
	webSearch      WebSearchOpts
	nonStreamingFn func() bool
	noaEnabledFn   func() bool
	maxTokensFn    func() int
}

func newModelRuntime(prov llm.Provider, model, workDir string, tx *transcript.Store, window int) modelRuntime {
	return modelRuntime{prov: prov, model: model, workDir: workDir, tx: tx, window: window}
}
func (runtime *modelRuntime) setNonStreaming(fn func() bool) { runtime.nonStreamingFn = fn }
func (runtime *modelRuntime) setNoaEnabled(fn func() bool)   { runtime.noaEnabledFn = fn }
func (runtime *modelRuntime) setMaxTokens(fn func() int)     { runtime.maxTokensFn = fn }
func (runtime *modelRuntime) setProxy(addr, caCert string) {
	runtime.proxyAddr, runtime.proxyCACert = addr, caCert
}
func (runtime *modelRuntime) setWebSearch(options WebSearchOpts) { runtime.webSearch = options }
func (runtime *modelRuntime) nonStreaming() bool {
	return runtime.nonStreamingFn != nil && runtime.nonStreamingFn()
}
func (runtime *modelRuntime) maxTokens() int {
	if runtime.maxTokensFn == nil {
		return 0
	}
	return runtime.maxTokensFn()
}
func (runtime *modelRuntime) compactionWindow(resolve *func() int) int {
	if *resolve != nil {
		return (*resolve)()
	}
	return runtime.window
}

// Resolve the two model flags in their original adjacent evaluation order.
func (runtime *modelRuntime) bindModel(options *agentcore.Options) {
	options.NonStreaming = runtime.nonStreaming()
	options.MaxTokens = runtime.maxTokens()
}

// Binding the original ID statements is lazy: no transcript means no ID work.
func (runtime *modelRuntime) bindTranscript(options *agentcore.Options, bindID func()) {
	if runtime.tx != nil {
		options.Transcript = runtime.tx
		bindID()
	}
}
