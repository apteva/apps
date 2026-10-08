package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const codexObservationEffort = "low"

// Use the existing raw Responses tool for Codex: older platform chat adapters
// drop reasoning controls. This keeps the effort explicit without a platform
// upgrade or changes to other providers. Media only builds system/user inputs.
func observationRequest(bound *sdk.BoundIntegration, model string, messages []map[string]any, maxTokens int, temperature float64) (string, map[string]any) {
	if bound.AppSlug != "openai-codex" {
		return bound.ToolFor("chat.complete"), map[string]any{
			"model": model, "messages": messages, "max_tokens": maxTokens, "temperature": temperature,
		}
	}
	var instructions []string
	input := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		role, _ := message["role"].(string)
		if role == "system" {
			text, _ := message["content"].(string)
			instructions = append(instructions, text)
			continue
		}
		parts := []map[string]any{}
		switch content := message["content"].(type) {
		case string:
			parts = append(parts, map[string]any{"type": "input_text", "text": content})
		case []map[string]any:
			for _, part := range content {
				switch part["type"] {
				case "text":
					parts = append(parts, map[string]any{"type": "input_text", "text": part["text"]})
				case "image_url":
					image, _ := part["image_url"].(map[string]any)
					p := map[string]any{"type": "input_image", "image_url": image["url"]}
					if detail, ok := image["detail"]; ok {
						p["detail"] = detail
					}
					parts = append(parts, p)
				}
			}
		}
		input = append(input, map[string]any{"type": "message", "role": role, "content": parts})
	}
	// Preserve the previous Codex chat adapter's omission of max_output_tokens;
	// the existing overall request timeout continues to bound these calls.
	return "responses_create", map[string]any{
		"model": model, "instructions": strings.Join(instructions, "\n\n"), "input": input,
		"reasoning": map[string]any{"effort": codexObservationEffort}, "store": false, "stream": true,
	}
}

func extractObservationContent(provider string, data json.RawMessage) (string, error) {
	if provider != "openai-codex" {
		return extractChatContent(data)
	}
	var response struct {
		Status    string          `json:"status"`
		Error     json.RawMessage `json:"error"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Status  string `json:"status"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("decode Codex response: %w", err)
	}
	if response.Status != "completed" || (len(response.Error) > 0 && string(response.Error) != "null") {
		return "", fmt.Errorf("Codex response is not successfully completed (status=%s)", response.Status)
	}
	if response.Reasoning.Effort != codexObservationEffort {
		return "", fmt.Errorf("Codex response did not confirm low reasoning effort (effort=%q)", response.Reasoning.Effort)
	}
	var text []string
	for _, item := range response.Output {
		if item.Type != "message" || item.Role != "assistant" || item.Status != "completed" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text = append(text, part.Text)
			}
		}
	}
	answer := strings.TrimSpace(strings.Join(text, "\n"))
	if answer == "" {
		return "", errors.New("Codex response contains no completed assistant answer")
	}
	return answer, nil
}
