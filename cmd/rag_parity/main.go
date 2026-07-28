package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type parityCase struct {
	Name     string
	Question string
	Provider string
}

type simpleResult struct {
	Question   string
	AnswerMode string
	Answer     string
	Sources    int
}

func main() {
	pythonBase := getenv("PY_RAG_URL", "http://127.0.0.1:8098/query")
	goBase := getenv("GO_RAG_URL", "http://127.0.0.1:8012/api/rag/go/query")
	minGoSources := getenvInt("MIN_GO_SOURCES", 1)
	maxGoSources := getenvInt("MAX_GO_SOURCES", 6)

	cases := []parityCase{
		{
			Name:     "prudential_room_and_board_limit",
			Question: "What is the annual limit for Prudential room and board?",
			Provider: "prudential",
		},
		{
			Name:     "bluecross_consult_coverage_zh",
			Question: "Blue Cross 包唔包獸醫診症？",
			Provider: "bluecross",
		},
		{
			Name:     "compare_consult_limits",
			Question: "Compare Blue Cross and Prudential veterinary consultation limits.",
			Provider: "",
		},
		{
			Name:     "waiting_period_meaning",
			Question: "What is the meaning of waiting period?",
			Provider: "",
		},
	}

	client := &http.Client{Timeout: 90 * time.Second}
	allOK := true
	for _, c := range cases {
		py, pyErr := query(client, pythonBase, c)
		goRes, goErr := query(client, goBase, c)

		fmt.Printf("=== %s ===\n", c.Name)
		if pyErr != nil {
			allOK = false
			fmt.Printf("python_error: %v\n", pyErr)
		} else {
			fmt.Printf("python_mode: %s\n", py.AnswerMode)
			fmt.Printf("python_sources: %d\n", py.Sources)
		}

		if goErr != nil {
			allOK = false
			fmt.Printf("go_error: %v\n", goErr)
		} else {
			fmt.Printf("go_mode: %s\n", goRes.AnswerMode)
			fmt.Printf("go_sources: %d\n", goRes.Sources)
		}

		if pyErr == nil && goErr == nil {
			modeMatch := py.AnswerMode == goRes.AnswerMode
			sourceRangeOK := goRes.Sources >= minGoSources && goRes.Sources <= maxGoSources
			nonEmptyAnswer := strings.TrimSpace(goRes.Answer) != ""
			fmt.Printf("mode_match: %v\n", modeMatch)
			fmt.Printf("go_sources_in_range[%d..%d]: %v\n", minGoSources, maxGoSources, sourceRangeOK)
			fmt.Printf("go_answer_non_empty: %v\n", nonEmptyAnswer)
			if !modeMatch || !sourceRangeOK || !nonEmptyAnswer {
				allOK = false
			}
		}
		fmt.Println()
	}

	if !allOK {
		os.Exit(2)
	}
}

func query(client *http.Client, base string, c parityCase) (simpleResult, error) {
	u, err := url.Parse(base)
	if err != nil {
		return simpleResult{}, err
	}
	q := u.Query()
	q.Set("q", c.Question)
	q.Set("max_sources", "3")
	if strings.TrimSpace(c.Provider) != "" {
		q.Set("provider", c.Provider)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return simpleResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return simpleResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return simpleResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return simpleResult{}, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		return simpleResult{}, err
	}
	result := simpleResult{
		Question: stringValue(generic, "question"),
		Answer:   stringValue(generic, "answer"),
	}
	result.AnswerMode = stringValue(generic, "answer_mode")
	if result.AnswerMode == "" {
		result.AnswerMode = stringValue(generic, "mode")
	}
	if arr, ok := generic["sources"].([]any); ok {
		result.Sources = len(arr)
	}
	return result, nil
}

func stringValue(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func getenv(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func getenvInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val <= 0 {
		return fallback
	}
	return val
}
