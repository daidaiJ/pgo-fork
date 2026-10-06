package agentcore

import "context"

// agentContextKey is the unexported context key under which the running loop
// exposes its AgentContext to tools.
type agentContextKey struct{}

// WithAgentContext returns a child context carrying agentCtx, so a tool that
// must read (or, for projection-metadata markers, append to) the live
// conversation can reach it. The loop injects this alongside the progress
// emitter before executing a batch; side contexts (tests, direct calls) carry
// none and AgentContextFromContext returns nil.
func WithAgentContext(ctx context.Context, agentCtx *AgentContext) context.Context {
	return context.WithValue(ctx, agentContextKey{}, agentCtx)
}

// AgentContextFromContext returns the running loop's AgentContext carried by
// ctx, or nil when the tool was invoked outside a loop-injected context.
func AgentContextFromContext(ctx context.Context) *AgentContext {
	agentCtx, _ := ctx.Value(agentContextKey{}).(*AgentContext)
	return agentCtx
}
