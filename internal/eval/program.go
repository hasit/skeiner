package eval

import "context"

// Program evaluates the same saved JavaScript body against each owner's case.
type Program struct {
	Code string
	Mode string
}

func (Program) Name() string { return "saved-program" }

func (p Program) Answer(ctx context.Context, task TaskInput, client Client) (Answer, error) {
	args := map[string]any{"code": p.Code}
	if p.Mode != "" {
		args["mode"] = p.Mode
	}
	if task.Input != nil {
		args["input"] = task.Input
	}
	raw, err := client.Call(ctx, "execute", args)
	if err != nil {
		return Answer{}, err
	}
	return ParseAnswer(raw, task)
}
