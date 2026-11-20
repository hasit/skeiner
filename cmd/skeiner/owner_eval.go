package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/hasit/skeiner/internal/eval"
)

// ownerEvaluation separates public task inputs from private grading data for
// external experiment runners. Receipts are trusted owner observations, not
// model-supplied attestations; this command does not authenticate their origin.
func ownerEvaluation(mode string, args []string, out, diag io.Writer) error {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(diag)
	suite := fs.String("suite", "", "owner-side task suite")
	caseID := fs.String("case", "", "case ID (required for grade)")
	var input string
	if mode == "grade" {
		fs.StringVar(&input, "input", "", "owner receipt JSON containing answer and exchanges")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suite == "" || fs.NArg() != 0 || mode == "grade" && (*caseID == "" || input == "") {
		return errors.New("expected --suite and, for grade, --case and --input")
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if mode == "tasks" {
		tasks, err := eval.LoadTaskInputs(*suite, *caseID)
		if err != nil {
			return err
		}
		type publicTask struct {
			eval.TaskInput
			Instruction string `json:"instruction"`
		}
		public := make(map[string]publicTask, len(tasks))
		for id, task := range tasks {
			public[id] = publicTask{task, eval.AnswerInstruction(task)}
		}
		return enc.Encode(public)
	}
	data, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var receipt struct {
		Answer    json.RawMessage `json:"answer"`
		Exchanges []eval.Exchange `json:"exchanges"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&receipt); err != nil {
		return errors.New("invalid owner receipt JSON")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("owner receipt must contain one JSON object")
	}
	tasks, err := eval.LoadTaskInputs(*suite, *caseID)
	if err != nil {
		return err
	}
	answer, err := eval.ParseAnswer(receipt.Answer, tasks[*caseID])
	if err != nil {
		return err
	}
	result, err := eval.GradeAnswer(*suite, *caseID, answer, receipt.Exchanges)
	if err != nil {
		return err
	}
	if err := enc.Encode(result); err != nil {
		return err
	}
	if !result.Passed {
		return errors.New("evaluation failed; inspect checks and diagnosis in the JSON report")
	}
	return nil
}
