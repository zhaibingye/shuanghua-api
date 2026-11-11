package common

import (
	appcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// GuessRelayFormatFromRequest moved to convmeta with the converters; the
// delegation keeps host callers unchanged.
func GuessRelayFormatFromRequest(req any) (types.RelayFormat, bool) {
	return convmeta.GuessRelayFormatFromRequest(req)
}

// MarshalConvertedTextRequest also handles native OpenAI DTOs returned by
// adaptors that forward the request without a protocol conversion.
func MarshalConvertedTextRequest(info *RelayInfo, request any) ([]byte, error) {
	switch req := request.(type) {
	case dto.GeneralOpenAIRequest:
		return MarshalConvertedTextRequest(info, &req)
	case *dto.GeneralOpenAIRequest:
		previous := req.ReasoningEffort
		if err := reasoning.NormalizeOpenAIChatBudget(req); err != nil {
			return nil, err
		}
		if info != nil && req.ReasoningEffort != previous {
			info.SetReasoningEffort(req.ReasoningEffort)
		}
	case dto.OpenAIResponsesRequest:
		return MarshalConvertedTextRequest(info, &req)
	case *dto.OpenAIResponsesRequest:
		previous := req.Reasoning
		reasoning.NormalizeOpenAIResponsesBudget(req)
		if info != nil && req.Reasoning != previous && req.Reasoning != nil {
			info.SetReasoningEffort(req.Reasoning.Effort)
		}
	}
	return appcommon.Marshal(request)
}

func AppendRequestConversionFromRequest(info *RelayInfo, req any) {
	if info == nil {
		return
	}
	format, ok := GuessRelayFormatFromRequest(req)
	if !ok {
		return
	}
	info.AppendRequestConversion(format)
}
