package main

import (
	"testing"
)

var testArgs = &CortexArgs{"Build Conway's Game of Life"}

func buildTestRequest() *AgentRequest {
	return testArgs.Request()
}

func TestCortexArgs(t *testing.T) {

	expectedRole := "user"
	expectedMessage := "Build Conway's Game of Life"
	req := testArgs.Request()

	if len(req.Messages) < 1 {
		t.Fatalf("expected messages but got none")
	}

	msg := req.Messages[0]

	parsedMessage := msg.Content
	if parsedMessage != expectedMessage {
		t.Errorf("message: expected %s but got %s", expectedMessage, parsedMessage)
	}

	parsedRole := msg.Role
	if parsedRole != expectedRole {
		t.Errorf("role: expected %s but got %s", expectedRole, parsedRole)
	}
}

func TestAgentRequestSend(t *testing.T) {
	req := testArgs.Request()
	res, err := req.Send()
	if err != nil {
		t.Error(err)
	}

	t.Errorf("response: %s", res.Choices[0])
}
