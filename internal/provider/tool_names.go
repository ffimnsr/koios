package provider

import "github.com/ffimnsr/koios/internal/types"

func encodeProviderToolDefinitions(tools []types.Tool) []types.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]types.Tool, len(tools))
	for i, tool := range tools {
		out[i] = tool
		out[i].Function.Name = types.EncodeProviderToolName(tool.Function.Name)
	}
	return out
}

func encodeProviderToolMessages(messages []types.Message) []types.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]types.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		if len(msg.ToolCalls) == 0 {
			continue
		}
		out[i].ToolCalls = make([]types.ToolCall, len(msg.ToolCalls))
		for j, call := range msg.ToolCalls {
			out[i].ToolCalls[j] = call
			out[i].ToolCalls[j].Function.Name = types.EncodeProviderToolName(call.Function.Name)
		}
	}
	return out
}

func decodeProviderToolCalls(resp *types.ChatResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Choices {
		for j := range resp.Choices[i].Message.ToolCalls {
			resp.Choices[i].Message.ToolCalls[j].Function.Name = types.DecodeProviderToolName(resp.Choices[i].Message.ToolCalls[j].Function.Name)
		}
	}
}
