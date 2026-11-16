package main

// Types of agentgateway's Guardrail Webhook API (POST /request, POST /response).
// The action is untagged: agentgateway tells mask, reject and pass apart by
// their fields, so each action marshals only its own.

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type PromptRequest struct {
	Body struct {
		Messages []Message `json:"messages"`
	} `json:"body"`
}

type ResponseChoice struct {
	Message Message `json:"message"`
}

type ResponseRequest struct {
	Body struct {
		Choices []ResponseChoice `json:"choices"`
	} `json:"body"`
}

// PassAction lets the content through unchanged.
type PassAction struct {
	Reason string `json:"reason,omitempty"`
}

// MaskAction replaces the content. Body is {"messages": [...]} on /request and
// {"choices": [...]} on /response, with as many entries as were sent.
type MaskAction struct {
	Body   any    `json:"body"`
	Reason string `json:"reason,omitempty"`
}

// RejectAction answers the client with StatusCode and Body instead.
type RejectAction struct {
	Body       string `json:"body"`
	StatusCode int    `json:"status_code"`
	Reason     string `json:"reason,omitempty"`
}

type ActionResponse struct {
	Action any `json:"action"`
}
