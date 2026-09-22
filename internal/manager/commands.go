package manager

import "context"

const managerCommandQueueSize = 64

type commandResult struct {
	result any
	err    *apiError
}

type managerCommand struct {
	ctx     context.Context
	execute func(context.Context, *manager) commandResult
	reply   chan commandResult
}

// submitCommand is the only path for asynchronous control-plane work to reach
// mutable manager state. A full mailbox affects only the caller; it never
// blocks reconciliation or an unrelated client.
func (m *manager) submitCommand(ctx context.Context, execute func(context.Context, *manager) commandResult) commandResult {
	if ctx == nil {
		ctx = context.Background()
	}
	command := managerCommand{ctx: ctx, execute: execute, reply: make(chan commandResult, 1)}
	select {
	case <-ctx.Done():
		return commandDeadlineResult()
	default:
	}
	select {
	case m.commands <- command:
	default:
		return commandQueueFullResult()
	}
	select {
	case result := <-command.reply:
		return result
	case <-ctx.Done():
		return commandDeadlineResult()
	}
}

func (m *manager) executeCommand(command managerCommand) {
	result := commandDeadlineResult()
	if command.ctx.Err() == nil && command.execute != nil {
		result = command.execute(command.ctx, m)
	}
	select {
	case command.reply <- result:
	default:
	}
}

func commandQueueFullResult() commandResult {
	return commandResult{err: &apiError{Code: "resource_exhausted", Message: "manager command queue is full"}}
}

func commandDeadlineResult() commandResult {
	return commandResult{err: &apiError{Code: "deadline_exceeded", Message: "request deadline expired before the manager could process it"}}
}
