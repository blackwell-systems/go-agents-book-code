package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch12/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

// live runs each version once against OpenRouter, recording every call
// to dir under the version's name as the script, and every person's
// decision as an approval. It needs OPENROUTER_API_KEY.
func live(ctx context.Context, out io.Writer, dir string) error {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		return errors.New("live: set OPENROUTER_API_KEY")
	}
	for _, v := range versions {
		c := task.Config{
			BaseURL: "https://openrouter.ai/api/v1",
			APIKey:  key,
			HTTP: &http.Client{Transport: &replay.Recorder{
				Dir: dir}},
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		env := &task.Env{}
		_, err := v.Run(replay.WithScript(ctx, v.Dir), c, env)
		cancel()
		r := env.Report()
		calls, first, cost, rerr := recorded(dir, v.Dir)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(out, "%s: %d calls, first prompt %d tokens, $%.4f; "+
			"issues %d, diagnosis %v, err %v\n", v.Name, calls, first,
			cost, r.Issues, r.Diagnosis != nil, err)
	}
	return nil
}

// A usage is what a recorded response says a call cost.
type usage struct {
	Status   int `json:"status"`
	Response struct {
		Usage struct {
			Prompt     int     `json:"prompt_tokens"`
			Completion int     `json:"completion_tokens"`
			Cost       float64 `json:"cost"`
		} `json:"usage"`
	} `json:"response"`
}

// recorded sums the calls recorded in dir for one script.
func recorded(dir, script string) (calls, first int, cost float64,
	err error) {
	for n := 1; ; n++ {
		file := filepath.Join(dir, fmt.Sprintf("%s-%02d.json", script, n))
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			return calls, first, cost, nil
		}
		if err != nil {
			return 0, 0, 0, fmt.Errorf("recorded: %w", err)
		}
		var u usage
		if err := json.Unmarshal(data, &u,
			json.RejectUnknownMembers(false)); err != nil {
			return 0, 0, 0, fmt.Errorf("recorded %s: %w", file, err)
		}
		calls++
		if n == 1 {
			first = u.Response.Usage.Prompt
		}
		cost += u.Response.Usage.Cost
	}
}
