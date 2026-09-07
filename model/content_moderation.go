package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	ModerationEventActive        = "active"
	ModerationEventFalsePositive = "false_positive"
	ModerationEventReversed      = "reversed"

	ModerationEventSourcePreflight  = "preflight"
	ModerationEventSourcePostflight = "postflight"

	ModerationEventActorUser      = "user"
	ModerationEventActorAssistant = "assistant"

	ModerationEventDecisionBlock = "block"
	ModerationEventDecisionAllow = "allow"
)

var (
	ErrModerationBlocked            = errors.New("request blocked by content moderation")
	ErrModerationAccountNotDisabled = errors.New("user was not disabled by content moderation")
	ErrModerationUserHistoryOnly    = errors.New("moderation user record is not historical")
)

// ModerationEvent is a single flagged request or assistant output. Clean
// traffic is never written here.
type ModerationEvent struct {
	ID                 int64   `json:"id" gorm:"primaryKey"`
	UserID             int     `json:"user_id" gorm:"not null;index:idx_moderation_events_user_created,priority:1"`
	RequestID          string  `json:"request_id" gorm:"type:varchar(128);index"`
	ChannelID          int     `json:"channel_id,omitempty" gorm:"index"`
	Model              string  `json:"model" gorm:"type:varchar(128)"`
	RelayFormat        string  `json:"relay_format" gorm:"type:varchar(32)"`
	Source             string  `json:"source" gorm:"type:varchar(16);not null;index"`
	Actor              string  `json:"actor" gorm:"type:varchar(16);not null"`
	Decision           string  `json:"decision" gorm:"type:varchar(16);not null"`
	Severity           string  `json:"severity" gorm:"type:varchar(16);not null"`
	Categories         string  `json:"categories" gorm:"type:text"`
	Confidence         float64 `json:"confidence"`
	ReasonCode         string  `json:"reason_code" gorm:"type:varchar(128)"`
	UserExcerpt        string  `json:"user_excerpt" gorm:"type:text"`
	AssistantExcerpt   string  `json:"assistant_excerpt" gorm:"type:text"`
	ContentFingerprint string  `json:"-" gorm:"type:char(64);index"`
	ImageCount         int     `json:"image_count"`
	Status             string  `json:"status" gorm:"type:varchar(24);not null;index"`
	ResolvedAt         int64   `json:"resolved_at,omitempty" gorm:"not null;default:0"`
	ResolvedBy         int     `json:"resolved_by,omitempty" gorm:"not null;default:0"`
	ResolutionNote     string  `json:"resolution_note,omitempty" gorm:"type:text"`
	CreatedAt          int64   `json:"created_at" gorm:"autoCreateTime;not null;index:idx_moderation_events_user_created,priority:2"`
	ExpiresAt          int64   `json:"expires_at" gorm:"not null;index"`
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
	ID               int64 `json:"id" gorm:"primaryKey"`
	UserID           int   `json:"user_id" gorm:"not null;uniqueIndex"`
	PreviousStatus   int   `json:"previous_status" gorm:"not null"`
	CreatedAt        int64 `json:"created_at" gorm:"autoCreateTime;not null"`
	RestoredAt       int64 `json:"restored_at,omitempty" gorm:"not null;default:0"`
	ManualDisabledAt int64 `json:"-" gorm:"not null;default:0"`
}

// ModerationUserRecord stores the operator-facing moderation note for a user.
// When OverrideActive is set, the displayed count is Override plus new user
// events created after OverrideAt. Setting the count to 0 archives the note.
type ModerationUserRecord struct {
	ID                     int64  `json:"id" gorm:"primaryKey"`
	UserID                 int    `json:"user_id" gorm:"not null;uniqueIndex"`
	ViolationCountOverride int    `json:"-" gorm:"not null"`
	OverrideActive         bool   `json:"-" gorm:"not null"`
	OverrideAt             int64  `json:"-" gorm:"not null;default:0"`
	MaxViolationCount      int    `json:"max_violation_count" gorm:"not null"`
	LastViolationAt        int64  `json:"last_violation_at" gorm:"not null;index"`
	UsernameSnapshot       string `json:"username" gorm:"type:varchar(128)"`
	DisplayNameSnapshot    string `json:"display_name" gorm:"type:varchar(128)"`
	EmailSnapshot          string `json:"email" gorm:"type:varchar(255)"`
	Note                   string `json:"note" gorm:"type:text"`
	ArchivedAt             int64  `json:"archived_at,omitempty" gorm:"not null;index"`
	CreatedAt              int64  `json:"created_at" gorm:"autoCreateTime;not null"`
	UpdatedAt              int64  `json:"updated_at" gorm:"autoUpdateTime;not null"`
}

type ModerationAction struct {
	ID        int64  `json:"id" gorm:"primaryKey"`
	AdminID   int    `json:"admin_id" gorm:"not null;index"`
	UserID    int    `json:"user_id,omitempty" gorm:"index"`
	EventID   int64  `json:"event_id,omitempty" gorm:"index"`
	Action    string `json:"action" gorm:"type:varchar(32);not null"`
	Reason    string `json:"reason,omitempty" gorm:"type:text"`
	CreatedAt int64  `json:"created_at" gorm:"autoCreateTime;not null;index"`
}

func GetModerationEvent(id int64) (*ModerationEvent, error) {
	if id <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var event ModerationEvent
	if err := DB.First(&event, id).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

func GetModerationUserRecord(userID int) (*ModerationUserRecord, error) {
	if userID <= 0 {
		return nil, errors.New("invalid moderation user")
	}
	var record ModerationUserRecord
	result := DB.Where("user_id = ?", userID).Limit(1).Find(&record)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &record, nil
}

func CountRecentUserModerationEvents(userID int, cutoff int64) (int64, error) {
	return CountRecentUserModerationEventsWithTx(DB, userID, cutoff)
}

func CountRecentUserModerationEventsWithTx(tx *gorm.DB, userID int, cutoff int64) (int64, error) {
	if tx == nil || userID <= 0 {
		return 0, errors.New("invalid moderation user database or user")
	}
	now := common.GetTimestamp()
	if cutoff <= 0 {
		cutoff = now - 7*24*60*60
	}
	var count int64
	err := tx.Model(&ModerationEvent{}).
		Where("user_id = ? AND actor = ? AND decision = ? AND status = ? AND created_at >= ? AND expires_at > ?", userID, ModerationEventActorUser, ModerationEventDecisionBlock, ModerationEventActive, cutoff, now).
		Count(&count).Error
	return count, err
}

func CountUserModerationEventsAfter(userID int, after, cutoff, now int64) (int64, error) {
	return countUserModerationEventsAfterWithTx(DB, userID, after, cutoff, now)
}

func countUserModerationEventsAfterWithTx(tx *gorm.DB, userID int, after, cutoff, now int64) (int64, error) {
	if tx == nil || userID <= 0 {
		return 0, errors.New("invalid moderation user database or user")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	query := tx.Model(&ModerationEvent{}).
		Where("user_id = ? AND actor = ? AND decision = ? AND status = ? AND expires_at > ?", userID, ModerationEventActorUser, ModerationEventDecisionBlock, ModerationEventActive, now)
	if cutoff > 0 {
		query = query.Where("created_at >= ?", cutoff)
	}
	if after > 0 {
		query = query.Where("created_at > ?", after)
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}

// DeleteModerationUserData completely removes all moderation events and user record for a user.
func DeleteModerationUserData(userID int) error {
	if userID <= 0 {
		return errors.New("invalid moderation user")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&ModerationEvent{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&ModerationUserRecord{}).Error; err != nil {
			return err
		}
		return nil
	})
}

// DeleteModerationUserHistoryIfArchived rechecks the current active event
// set under a row lock immediately before deleting a history record.
func DeleteModerationUserHistoryIfArchived(userID int, cutoff, now int64) error {
	if userID <= 0 {
		return errors.New("invalid moderation user")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if cutoff <= 0 {
		cutoff = now - 7*24*60*60
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var record ModerationUserRecord
		if err := lockForUpdate(tx).Where("user_id = ?", userID).First(&record).Error; err != nil {
			return err
		}
		if record.ArchivedAt == 0 {
			return ErrModerationUserHistoryOnly
		}
		after := int64(0)
		if record.OverrideActive {
			after = record.OverrideAt
		}
		count, err := countUserModerationEventsAfterWithTx(tx, userID, after, cutoff, now)
		if err != nil {
			return err
		}
		if count > 0 {
			return ErrModerationUserHistoryOnly
		}
		return tx.Delete(&record).Error
	})
}

// SetUserAccountStatusForModeration applies an explicit administrator status
// change without allowing a later automated moderation retry to undo a manual
// disable. If the account was disabled by moderation, enabling it also restores
// only the tokens owned by that moderation state.
func SetUserAccountStatusForModeration(userID, status int, now int64) error {
	if userID <= 0 || (status != common.UserStatusEnabled && status != common.UserStatusDisabled) {
		return errors.New("invalid moderation user status")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		var accountState ModerationAccountState
		stateErr := tx.Where("user_id = ? AND restored_at = 0", userID).First(&accountState).Error
		if stateErr != nil && !errors.Is(stateErr, gorm.ErrRecordNotFound) {
			return stateErr
		}
		if status == common.UserStatusDisabled {
			if stateErr == nil {
				if err := tx.Model(&accountState).Update("manual_disabled_at", now).Error; err != nil {
					return err
				}
			}
		} else if stateErr == nil {
			if err := restoreTokensAfterModeration(tx, userID, now); err != nil {
				return err
			}
			if err := tx.Model(&accountState).Updates(map[string]any{
				"manual_disabled_at": 0,
				"restored_at":        now,
			}).Error; err != nil {
				return err
			}
		}
		if user.Status == status {
			return nil
		}
		if _, err := IncrementUserAuthVersionWithTx(tx, userID); err != nil {
			return err
		}
		return tx.Model(&User{}).Where("id = ?", userID).Update("status", status).Error
	})
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
			changed = accountState.ManualDisabledAt == 0
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
			if err := tx.Where("user_id = ?", userID).First(&accountState).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			changed = true
			return nil
		}
		if accountState.ManualDisabledAt > 0 {
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

func DeleteExpiredModerationData(now, retentionSeconds int64) error {
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if retentionSeconds <= 0 {
		retentionSeconds = 7 * 24 * 60 * 60
	}
	cutoff := now - retentionSeconds
	if err := DB.Where("expires_at <= ? OR created_at <= ?", now, cutoff).Delete(&ModerationEvent{}).Error; err != nil {
		return err
	}
	if err := DB.Where("created_at <= ?", cutoff).Delete(&ModerationAction{}).Error; err != nil {
		return err
	}
	if err := DB.Where("restored_at > 0 AND created_at <= ?", cutoff).Delete(&ModerationTokenState{}).Error; err != nil {
		return err
	}
	return DB.Where("restored_at > 0 AND created_at <= ?", cutoff).Delete(&ModerationAccountState{}).Error
}

var legacyModerationTables = []string{
	"moderation_notifications",
	"moderation_jobs",
	"moderation_turns",
	"moderation_violations",
	"moderation_conversations",
}

// DropLegacyModerationTables removes the conversation-centric tables from the
// previous moderation design. Existing rows are not migrated.
func DropLegacyModerationTables(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	for _, table := range legacyModerationTables {
		if !db.Migrator().HasTable(table) {
			continue
		}
		if err := db.Migrator().DropTable(table); err != nil {
			return fmt.Errorf("drop legacy moderation table %s: %w", table, err)
		}
	}
	return nil
}

func migrateModerationUserRecordOverrideAt(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&ModerationUserRecord{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&ModerationUserRecord{}, "override_at") {
		return nil
	}
	now := common.GetTimestamp()
	return db.Model(&ModerationUserRecord{}).
		Where("override_active = ? AND override_at = ?", true, 0).
		Update("override_at", now).Error
}
