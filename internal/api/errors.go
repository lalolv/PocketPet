package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/store"
)

// 业务错误码。
const (
	codeBadRequest    = "bad_request"
	codeNotFound      = "not_found"
	codeInvalidAction = "invalid_action"
	codeLowEnergy     = "low_energy"
	codeInvalidState  = "invalid_state"
	codePetDead       = "pet_dead"
	codeLLMMissing    = "llm_not_configured" // A2A 消息端点在未配置 LLM 时返回（chat 走降级不返回它）
	codeInternal      = "internal"
)

// errorBody 是统一错误响应格式 {"error":{"code","message"}}。
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json failed", "err", err)
	}
}

// writeDomainError 把领域/存储错误映射为 4xx 业务错误，未知错误映射 500。
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "pet not found")
	case errors.Is(err, pet.ErrUnknownAction):
		writeError(w, http.StatusBadRequest, codeInvalidAction, err.Error())
	case errors.Is(err, pet.ErrLowEnergy):
		writeError(w, http.StatusConflict, codeLowEnergy, err.Error())
	case errors.Is(err, pet.ErrSleeping),
		errors.Is(err, pet.ErrAlreadySleeping),
		errors.Is(err, pet.ErrNotSleeping):
		writeError(w, http.StatusConflict, codeInvalidState, err.Error())
	case errors.Is(err, pet.ErrDead):
		writeError(w, http.StatusConflict, codePetDead, err.Error())
	default:
		slog.Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}
