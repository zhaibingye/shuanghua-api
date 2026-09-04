package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const (
	ModerationConversationActive   = "active"
	ModerationConversationBlocked  = "blocked"
	ModerationConversationResolved = "resolved"

	ModerationJobPending = "pending"
	ModerationJobRunning = "running"
	ModerationJobSuccess = "succeeded"
	ModerationJobFailed  = "failed"

	ModerationViolationActive        = "active"
	ModerationViolationFalsePositive = "false_positive"
	ModerationViolationReversed      = "reversed"

	ModerationNotificationPending = "pending"
	ModerationNotificationSent    = "sent"
	ModerationNotificationFailed  = "failed"
)

var (
	ErrModerationConversationBlocked = errors.New("content moderation blocked this conversation")
	ErrModerationAccountNotDisabled  = errors.New("user was not disabled by content moderation")
	ErrModerationJobLeaseLost        = errors.New("content moderation job lease lost")
)

type ModerationText string

func (ModerationText) GormDataType() string { return "text" }

func (ModerationText) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	if db != nil && db.Name() == "mysql" {
		return "longtext"
	}
	return "text"
}

// ModerationConversation keeps conversation-level state. The text itself is
// stored in ModerationTurn and expires seven days after the last activity.
// Conversation metadata is retained long enough to support the configured
// account-abuse window and administrator decisions.
type ModerationConversation struct {
	ID              int64  `json:"id" gorm:"primaryKey"`
	UserID          int    `json:"user_id" gorm:"not null;index:idx_moderation_conversations_user_activity,priority:1;uniqueIndex:idx_moderation_conversations_user_key,priority:1"`
	ConversationID  string `json:"conversation_id" gorm:"type:varchar(128);not null;uniqueIndex:idx_moderation_conversations_user_key,priority:2"`
	Status          string `json:"status" gorm:"type:varchar(16);not null;index"`
	FirstActivityAt int64  `json:"first_activity_at" gorm:"not null;index"`
	LastActivityAt  int64  `json:"last_activity_at" gorm:"not null;index:idx_moderation_conversations_user_activity,priority:2"`
	ExpiresAt       int64  `json:"expires_at" gorm:"not null;index"`
	BlockedAt       int64  `json:"blocked_at,omitempty" gorm:"not null;default:0"`
	BlockedReason   string `json:"blocked_reason,omitempty" gorm:"type:varchar(255)"`
	CreatedAt       int64  `json:"created_at" gorm:"autoCreateTime;not null"`
	UpdatedAt       int64  `json:"updated_at" gorm:"autoUpdateTime;not null"`
}

// ModerationTurn is the auditable request/response timeline. File and media
// bodies are deliberately reduced to placeholders before this record is
// written; binary data never enters this table.
type ModerationTurn struct {
	ID              int64          `json:"id" gorm:"primaryKey"`
	ConversationID  int64          `json:"conversation_row_id" gorm:"not null;index"`
	UserID          int            `json:"user_id" gorm:"not null;index;uniqueIndex:idx_moderation_turns_conversation_round,priority:1"`
	ConversationKey string         `json:"conversation_id" gorm:"type:varchar(128);not null;uniqueIndex:idx_moderation_turns_conversation_round,priority:2"`
	RoundNumber     int            `json:"round_number" gorm:"not null;uniqueIndex:idx_moderation_turns_conversation_round,priority:3"`
	ChannelID       int            `json:"channel_id,omitempty" gorm:"index"`
	RequestID       string         `json:"request_id" gorm:"type:varchar(128);index"`
	SystemPrompt    ModerationText `json:"system_prompt" gorm:"type:text"`
	UserPrompt      ModerationText `json:"user_prompt" gorm:"type:text"`
	AssistantReply  ModerationText `json:"assistant_reply" gorm:"type:text"`
	ResponseStatus  string         `json:"response_status" gorm:"type:varchar(24);not null"`
	RelayFormat     string         `json:"relay_format" gorm:"type:varchar(32)"`
	Model           string         `json:"model" gorm:"type:varchar(128)"`
	ReviewRequired  bool           `json:"review_required"`
	ReviewTrigger   string         `json:"review_trigger,omitempty" gorm:"type:varchar(64)"`
	CreatedAt       int64          `json:"created_at" gorm:"autoCreateTime;not null;index"`
	UpdatedAt       int64          `json:"updated_at" gorm:"autoUpdateTime;not null"`
	ExpiresAt       int64          `json:"expires_at" gorm:"not null;index"`
}

// ModerationTokenState remembers token statuses changed by moderation so an
// administrator can restore only those tokens, without re-enabling a token
// that was disabled independently.
type ModerationTokenState struct {
	ID             int64 `json:"id" gorm:"primaryKey"`
	UserID         int   `json:"user_id" gorm:"not null;index"`
	TokenID        int   `json:"token_id" gorm:"not null;uniqueIndex"`
	PreviousStatus int   `json:"previous_status" gorm:"not null"`
	CreatedAt      int64 `json:"created_at" gorm:"autoCreateTime;not null"`
	RestoredAt     int64 `json:"restored_at,omitempty" gorm:"not null;default:0"`
}

// ModerationAccountState distinguishes an account disabled by moderation from
// one that an administrator or another security workflow disabled.
type ModerationAccountState struct {
	ID             int64 `json:"id" gorm:"primaryKey"`
	UserID         int   `json:"user_id" gorm:"not null;uniqueIndex"`
	PreviousStatus int   `json:"previous_status" gorm:"not null"`
	CreatedAt      int64 `json:"created_at" gorm:"autoCreateTime;not null"`
	RestoredAt     int64 `json:"restored_at,omitempty" gorm:"not null;default:0"`
}

type ModerationJob struct {
	ID              int64          `json:"id" gorm:"primaryKey"`
	TurnID          int64          `json:"turn_id" gorm:"not null;uniqueIndex"`
	ConversationID  int64          `json:"conversation_row_id" gorm:"not null;index"`
	UserID          int            `json:"user_id" gorm:"not null;index"`
	Status          string         `json:"status" gorm:"type:varchar(16);not null;index"`
	Attempts        int            `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt   int64          `json:"next_attempt_at" gorm:"not null;index"`
	LockedAt        int64          `json:"locked_at,omitempty" gorm:"not null;default:0"`
	RequestPayload  ModerationText `json:"request_payload,omitempty" gorm:"type:text"`
	ResponsePayload ModerationText `json:"response_payload,omitempty" gorm:"type:text"`
	LastError       string         `json:"last_error,omitempty" gorm:"type:text"`
	Provider        string         `json:"provider" gorm:"type:varchar(32)"`
	Model           string         `json:"model" gorm:"type:varchar(128)"`
	PromptVersion   string         `json:"prompt_version" gorm:"type:varchar(32)"`
	ExpiresAt       int64          `json:"expires_at" gorm:"not null;index"`
	CreatedAt       int64          `json:"created_at" gorm:"autoCreateTime;not null;index"`
	UpdatedAt       int64          `json:"updated_at" gorm:"autoUpdateTime;not null"`
}

type ModerationViolation struct {
	ID             int64   `json:"id" gorm:"primaryKey"`
	UserID         int     `json:"user_id" gorm:"not null;index:idx_moderation_violations_user_created,priority:1"`
	ConversationID string  `json:"conversation_id" gorm:"type:varchar(128);not null;uniqueIndex:idx_moderation_violations_user_conversation_actor,priority:1"`
	TurnID         int64   `json:"turn_id" gorm:"not null;index"`
	Actor          string  `json:"actor" gorm:"type:varchar(16);not null;uniqueIndex:idx_moderation_violations_user_conversation_actor,priority:3"`
	UserViolation  bool    `json:"user_violation" gorm:"uniqueIndex:idx_moderation_violations_user_conversation_actor,priority:2"`
	Decision       string  `json:"decision" gorm:"type:varchar(16);not null"`
	Severity       string  `json:"severity" gorm:"type:varchar(16);not null"`
	Categories     string  `json:"categories" gorm:"type:text"`
	Confidence     float64 `json:"confidence"`
	ReasonCode     string  `json:"reason_code" gorm:"type:varchar(128)"`
	Status         string  `json:"status" gorm:"type:varchar(24);not null;index"`
	CreatedAt      int64   `json:"created_at" gorm:"autoCreateTime;not null;index:idx_moderation_violations_user_created,priority:2"`
	ExpiresAt      int64   `json:"expires_at" gorm:"not null;index"`
	ResolvedAt     int64   `json:"resolved_at,omitempty" gorm:"not null;default:0"`
	ResolvedBy     int     `json:"resolved_by,omitempty" gorm:"not null;default:0"`
	ResolutionNote string  `json:"resolution_note,omitempty" gorm:"type:text"`
}

type ModerationNotification struct {
	ID            int64  `json:"id" gorm:"primaryKey"`
	ViolationID   int64  `json:"violation_id" gorm:"not null;index"`
	AlertType     string `json:"alert_type" gorm:"type:varchar(32);not null;index"`
	Recipient     string `json:"recipient" gorm:"type:varchar(255);not null"`
	DedupeKey     string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_moderation_notifications_dedupe"`
	Status        string `json:"status" gorm:"type:varchar(16);not null;index"`
	Attempts      int    `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty" gorm:"not null;default:0;index"`
	LastError     string `json:"last_error,omitempty" gorm:"type:text"`
	CreatedAt     int64  `json:"created_at" gorm:"autoCreateTime;not null"`
	UpdatedAt     int64  `json:"updated_at" gorm:"autoUpdateTime;not null"`
}

type ModerationAction struct {
	ID             int64  `json:"id" gorm:"primaryKey"`
	AdminID        int    `json:"admin_id" gorm:"not null;index"`
	UserID         int    `json:"user_id,omitempty" gorm:"index"`
	ConversationID string `json:"conversation_id,omitempty" gorm:"type:varchar(128);index"`
	ViolationID    int64  `json:"violation_id,omitempty" gorm:"index"`
	Action         string `json:"action" gorm:"type:varchar(32);not null"`
	Reason         string `json:"reason,omitempty" gorm:"type:text"`
	CreatedAt      int64  `json:"created_at" gorm:"autoCreateTime;not null;index"`
}

func LockModerationConversation(tx *gorm.DB, userID int, conversationID string, conversation *ModerationConversation) error {
	if tx == nil || userID <= 0 || conversationID == "" || conversation == nil {
		return gorm.ErrRecordNotFound
	}
	return lockForUpdate(tx).Where("user_id = ? AND conversation_id = ?", userID, conversationID).First(conversation).Error
}

func GetModerationConversation(userID int, conversationID string) (*ModerationConversation, error) {
	if userID <= 0 || conversationID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var conversation ModerationConversation
	result := DB.Where("user_id = ? AND conversation_id = ?", userID, conversationID).Limit(1).Find(&conversation)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &conversation, nil
}

func IsModerationConversationBlocked(userID int, conversationID string) (bool, error) {
	conversation, err := GetModerationConversation(userID, conversationID)
	if err != nil || conversation == nil {
		return false, err
	}
	return conversation.Status == ModerationConversationBlocked, nil
}

func CountRecentUserModerationViolations(userID int, cutoff int64) (int64, error) {
	return CountRecentUserModerationViolationsWithTx(DB, userID, cutoff)
}

func CountRecentUserModerationViolationsWithTx(tx *gorm.DB, userID int, cutoff int64) (int64, error) {
	if tx == nil || userID <= 0 {
		return 0, errors.New("invalid moderation user database or user")
	}
	if cutoff <= 0 {
		cutoff = time.Now().Add(-7 * 24 * time.Hour).Unix()
	}
	var count int64
	err := tx.Model(&ModerationViolation{}).
		Where("user_id = ? AND user_violation = ? AND status = ? AND created_at >= ?", userID, true, ModerationViolationActive, cutoff).
		Distinct("conversation_id").Count(&count).Error
	return count, err
}

func RequeueStaleModerationJobs(now, staleBefore int64) error {
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if staleBefore <= 0 {
		staleBefore = now - 5*60
	}
	return DB.Model(&ModerationJob{}).
		Where("status = ? AND locked_at > 0 AND locked_at < ?", ModerationJobRunning, staleBefore).
		Updates(map[string]interface{}{
			"status":          ModerationJobPending,
			"next_attempt_at": now,
			"locked_at":       0,
			"last_error":      "moderation job lease expired",
			"updated_at":      now,
		}).Error
}

func FindPendingModerationJobs(now int64, limit int) ([]ModerationJob, error) {
	if limit <= 0 {
		limit = 20
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	var jobs []ModerationJob
	err := DB.Where("status = ? AND next_attempt_at <= ? AND expires_at > ?", ModerationJobPending, now, now).
		Order("id asc").Limit(limit).Find(&jobs).Error
	return jobs, err
}

func ClaimModerationJob(id int64, now int64) (*ModerationJob, bool, error) {
	if id <= 0 {
		return nil, false, errors.New("invalid moderation job")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	result := DB.Model(&ModerationJob{}).Where("id = ? AND status = ?", id, ModerationJobPending).
		Updates(map[string]interface{}{"status": ModerationJobRunning, "locked_at": now, "updated_at": now})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	var job ModerationJob
	if err := DB.First(&job, id).Error; err != nil {
		return nil, false, err
	}
	return &job, true, nil
}

func SaveModerationJobResult(id, lockedAt int64, status string, attempts int, nextAttemptAt int64, responsePayload, lastError string) error {
	if id <= 0 || lockedAt <= 0 {
		return errors.New("invalid moderation job lease")
	}
	var current ModerationJob
	leaseResult := DB.Select("id").Where("id = ? AND status = ? AND locked_at = ?", id, ModerationJobRunning, lockedAt).Limit(1).Find(&current)
	if leaseResult.Error != nil {
		return leaseResult.Error
	}
	if leaseResult.RowsAffected == 0 {
		return ErrModerationJobLeaseLost
	}
	encryptedResponsePayload, err := common.EncryptSecret(responsePayload)
	if err != nil {
		return err
	}
	result := DB.Model(&ModerationJob{}).Where("id = ? AND status = ? AND locked_at = ?", id, ModerationJobRunning, lockedAt).Updates(map[string]interface{}{
		"status":           status,
		"attempts":         attempts,
		"next_attempt_at":  nextAttemptAt,
		"response_payload": encryptedResponsePayload,
		"last_error":       lastError,
		"locked_at":        0,
		"updated_at":       common.GetTimestamp(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrModerationJobLeaseLost
	}
	return nil
}

func DeleteExpiredModerationContent(now int64) error {
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if err := DB.Where("expires_at <= ?", now).Delete(&ModerationJob{}).Error; err != nil {
		return err
	}
	if err := DB.Where("expires_at <= ?", now).Delete(&ModerationTurn{}).Error; err != nil {
		return err
	}
	return DB.Where("expires_at <= ?", now).Delete(&ModerationConversation{}).Error
}

func ListRetryableModerationNotifications(now int64, limit int) ([]ModerationNotification, error) {
	if limit <= 0 {
		limit = 50
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	var notifications []ModerationNotification
	err := DB.Where("status IN ? AND attempts < ? AND (next_attempt_at = 0 OR next_attempt_at <= ?)", []string{ModerationNotificationPending, ModerationNotificationFailed}, 5, now).
		Order("id asc").Limit(limit).Find(&notifications).Error
	return notifications, err
}

func disableTokensForModeration(tx *gorm.DB, userID int, now int64) error {
	var tokens []Token
	if err := tx.Where("user_id = ? AND status = ?", userID, common.TokenStatusEnabled).Find(&tokens).Error; err != nil {
		return err
	}
	for _, token := range tokens {
		var state ModerationTokenState
		err := tx.Where("token_id = ?", token.Id).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state = ModerationTokenState{
				UserID:         userID,
				TokenID:        token.Id,
				PreviousStatus: token.Status,
				CreatedAt:      now,
			}
			if err := tx.Create(&state).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&state).Updates(map[string]any{
			"user_id":         userID,
			"previous_status": token.Status,
			"created_at":      now,
			"restored_at":     0,
		}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&Token{}).Where("user_id = ? AND status = ?", userID, common.TokenStatusEnabled).
		Update("status", common.TokenStatusDisabled).Error
}

func restoreTokensAfterModeration(tx *gorm.DB, userID int, now int64) error {
	var states []ModerationTokenState
	if err := tx.Where("user_id = ? AND restored_at = 0", userID).Find(&states).Error; err != nil {
		return err
	}
	for _, state := range states {
		if err := tx.Model(&Token{}).
			Where("id = ? AND user_id = ? AND status = ?", state.TokenID, userID, common.TokenStatusDisabled).
			Update("status", state.PreviousStatus).Error; err != nil {
			return err
		}
		if err := tx.Model(&state).Update("restored_at", now).Error; err != nil {
			return err
		}
	}
	return nil
}

// DisableUserAndTokensForModeration atomically changes the account status and
// records/disables only currently enabled API tokens.
func DisableUserAndTokensForModeration(userID int, now int64) (bool, error) {
	if userID <= 0 {
		return false, errors.New("invalid moderation user")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			var accountState ModerationAccountState
			if err := tx.Where("user_id = ? AND restored_at = 0", userID).First(&accountState).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			changed = true
			return nil
		}
		var accountState ModerationAccountState
		stateErr := tx.Where("user_id = ?", userID).First(&accountState).Error
		if errors.Is(stateErr, gorm.ErrRecordNotFound) {
			accountState = ModerationAccountState{
				UserID:         userID,
				PreviousStatus: user.Status,
				CreatedAt:      now,
			}
		} else if stateErr != nil {
			return stateErr
		} else {
			accountState.PreviousStatus = user.Status
			accountState.CreatedAt = now
			accountState.RestoredAt = 0
		}
		if _, err := IncrementUserAuthVersionWithTx(tx, userID); err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where("id = ?", userID).Update("status", common.UserStatusDisabled).Error; err != nil {
			return err
		}
		if err := disableTokensForModeration(tx, userID, now); err != nil {
			return err
		}
		if accountState.ID == 0 {
			if err := tx.Create(&accountState).Error; err != nil {
				return err
			}
		} else if err := tx.Save(&accountState).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// RestoreUserAndTokensAfterModeration atomically restores the account and
// tokens previously disabled by moderation. Tokens changed independently are
// left untouched. The boolean reports whether moderation-owned account state
// exists, including an already-restored state used to finish retryable side
// effects such as cache and session invalidation.
func RestoreUserAndTokensAfterModeration(userID int, now int64) (bool, error) {
	if userID <= 0 {
		return false, errors.New("invalid moderation user")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		var accountState ModerationAccountState
		if err := tx.Where("user_id = ? AND restored_at = 0", userID).First(&accountState).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			// A previous restore may have committed before cache/session
			// invalidation failed. Treat that durable marker as an idempotent
			// success so a retry can finish those side effects.
			if err := tx.Where("user_id = ?", userID).First(&accountState).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			changed = true
			return nil
		}
		changed = true
		if user.Status != common.UserStatusEnabled {
			if _, err := IncrementUserAuthVersionWithTx(tx, userID); err != nil {
				return err
			}
			if err := tx.Model(&User{}).Where("id = ?", userID).Update("status", accountState.PreviousStatus).Error; err != nil {
				return err
			}
			changed = true
		}
		if err := restoreTokensAfterModeration(tx, userID, now); err != nil {
			return err
		}
		if err := tx.Model(&accountState).Update("restored_at", now).Error; err != nil {
			return err
		}
		return nil
	})
	return changed, err
}

func DeleteExpiredModerationMetadata(now int64, retentionSeconds ...int64) error {
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if err := DB.Where("expires_at <= ?", now).Delete(&ModerationViolation{}).Error; err != nil {
		return err
	}
	var retention int64 = 7 * 24 * 60 * 60
	if len(retentionSeconds) > 0 && retentionSeconds[0] > 0 {
		retention = retentionSeconds[0]
	}
	cutoff := now - retention
	if err := DB.Where("created_at <= ?", cutoff).Delete(&ModerationNotification{}).Error; err != nil {
		return err
	}
	if err := DB.Where("restored_at > 0 AND created_at <= ?", cutoff).Delete(&ModerationTokenState{}).Error; err != nil {
		return err
	}
	if err := DB.Where("restored_at > 0 AND created_at <= ?", cutoff).Delete(&ModerationAccountState{}).Error; err != nil {
		return err
	}
	return DB.Where("created_at <= ?", cutoff).Delete(&ModerationAction{}).Error
}
