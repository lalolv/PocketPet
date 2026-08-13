package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/lalolv/PocketPet/internal/httpx"
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

func writeError(w http.ResponseWriter, status int, code, msg string) {
	httpx.WriteError(w, status, code, msg)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	httpx.WriteJSON(w, status, v)
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
		errors.Is(err, pet.ErrNotSleeping),
		errors.Is(err, pet.ErrBusy),
		errors.Is(err, pet.ErrIncubating),
		errors.Is(err, pet.ErrNotIncubating):
		writeError(w, http.StatusConflict, codeInvalidState, err.Error())
	case errors.Is(err, pet.ErrDead):
		writeError(w, http.StatusConflict, codePetDead, err.Error())
	default:
		slog.Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}
