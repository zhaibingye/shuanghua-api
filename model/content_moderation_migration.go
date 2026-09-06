package model

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type moderationUniqueConstraint struct {
	Name       string `gorm:"column:constraint_name"`
	Definition string `gorm:"column:constraint_definition"`
	Deferrable bool   `gorm:"column:is_deferrable"`
	Validated  bool   `gorm:"column:is_validated"`
}

type moderationUniqueConstraintTarget struct {
	model      any
	columnName string
	indexName  string
}

var moderationUniqueConstraintTargets = []moderationUniqueConstraintTarget{
	{
		model:      &ModerationNotification{},
		columnName: "dedupe_key",
		indexName:  "idx_moderation_notifications_dedupe",
	},
	{
		model:      &ModerationTokenState{},
		columnName: "token_id",
		indexName:  "idx_moderation_token_states_token_id",
	},
	{
		model:      &ModerationAccountState{},
		columnName: "user_id",
		indexName:  "idx_moderation_account_states_user_id",
	},
	{
		model:      &ModerationUserRecord{},
		columnName: "user_id",
		indexName:  "idx_moderation_user_records_user_id",
	},
	{
		model:      &ModerationJob{},
		columnName: "turn_id",
		indexName:  "idx_moderation_jobs_turn_id",
	},
}

func inspectModerationUniqueConstraints(db *gorm.DB, tableName string, columnName string) ([]moderationUniqueConstraint, error) {
	var constraints []moderationUniqueConstraint
	if err := db.Raw(`
SELECT constraint_meta.conname AS constraint_name,
       pg_get_constraintdef(constraint_meta.oid) AS constraint_definition,
       constraint_meta.condeferrable AS is_deferrable,
       constraint_meta.convalidated AS is_validated
FROM pg_catalog.pg_constraint AS constraint_meta
WHERE constraint_meta.conrelid = to_regclass(?)
  AND constraint_meta.contype = 'u'
  AND cardinality(constraint_meta.conkey) = 1
  AND EXISTS (
      SELECT 1
      FROM pg_catalog.pg_attribute AS attribute_meta
      WHERE attribute_meta.attrelid = constraint_meta.conrelid
        AND attribute_meta.attnum = constraint_meta.conkey[1]
        AND attribute_meta.attname = ?
  )
ORDER BY constraint_meta.conname`, tableName, columnName).Scan(&constraints).Error; err != nil {
		return nil, fmt.Errorf("inspect moderation unique constraints for %s.%s: %w", tableName, columnName, err)
	}
	return constraints, nil
}

func inspectModerationStandaloneIndex(db *gorm.DB, tableName string, indexName string, columnName string) (bool, bool, error) {
	var state struct {
		Exists          bool `gorm:"column:index_exists"`
		StandaloneValid bool `gorm:"column:standalone_valid"`
	}
	if err := db.Raw(`
SELECT count(*) > 0 AS index_exists,
       COALESCE(bool_or(
           index_meta.indisunique
           AND index_meta.indisvalid
           AND index_meta.indisready
           AND NOT index_meta.indisprimary
           AND index_meta.indpred IS NULL
           AND index_meta.indexprs IS NULL
           AND index_meta.indnatts = 1
           AND attribute_meta.attname = ?
           AND NOT EXISTS (
               SELECT 1
               FROM pg_catalog.pg_constraint AS constraint_meta
               WHERE constraint_meta.conindid = index_meta.indexrelid
           )
       ), false) AS standalone_valid
FROM pg_catalog.pg_index AS index_meta
JOIN pg_catalog.pg_class AS index_class
  ON index_class.oid = index_meta.indexrelid
LEFT JOIN pg_catalog.pg_attribute AS attribute_meta
  ON attribute_meta.attrelid = index_meta.indrelid
 AND attribute_meta.attnum = index_meta.indkey[0]
WHERE index_meta.indrelid = to_regclass(?)
  AND index_class.relname = ?`, columnName, tableName, indexName).Scan(&state).Error; err != nil {
		return false, false, fmt.Errorf("inspect moderation standalone unique index for %s.%s: %w", tableName, indexName, err)
	}
	return state.Exists, state.StandaloneValid, nil
}

func migrateModerationTargetUniqueness(tx *gorm.DB, target moderationUniqueConstraintTarget, tableName string) error {
	migrator := tx.Migrator()
	if !migrator.HasTable(target.model) {
		return nil
	}

	if err := tx.Exec(
		"LOCK TABLE ? IN ACCESS EXCLUSIVE MODE",
		clause.Table{Name: tableName},
	).Error; err != nil {
		return fmt.Errorf("lock %s for uniqueness migration: %w", tableName, err)
	}

	constraints, err := inspectModerationUniqueConstraints(tx, tableName, target.columnName)
	if err != nil {
		return err
	}
	if len(constraints) == 0 {
		return nil
	}

	for _, constraint := range constraints {
		if err := tx.Exec(
			"ALTER TABLE ? DROP CONSTRAINT IF EXISTS ?",
			clause.Table{Name: tableName},
			clause.Column{Name: constraint.Name},
		).Error; err != nil {
			return fmt.Errorf("drop %s unique constraint %q: %w", tableName, constraint.Name, err)
		}
	}

	indexExists, standaloneValid, err := inspectModerationStandaloneIndex(tx, tableName, target.indexName, target.columnName)
	if err != nil {
		return err
	}
	if !standaloneValid {
		if indexExists {
			_ = migrator.DropIndex(target.model, target.indexName)
		}
		if err := migrator.CreateIndex(target.model, target.indexName); err != nil {
			return fmt.Errorf("create %s unique index %q: %w", tableName, target.indexName, err)
		}
		_, standaloneValid, err = inspectModerationStandaloneIndex(tx, tableName, target.indexName, target.columnName)
		if err != nil {
			return err
		}
		if !standaloneValid {
			return fmt.Errorf("%s unique index %q has an unexpected definition", tableName, target.indexName)
		}
	}

	remainingConstraints, err := inspectModerationUniqueConstraints(tx, tableName, target.columnName)
	if err != nil {
		return err
	}
	if len(remainingConstraints) != 0 {
		return fmt.Errorf("%s.%s still has unique constraints after migration", tableName, target.columnName)
	}

	return nil
}

// migrateModerationUniqueConstraints converts leftover PostgreSQL UNIQUE constraints
// on moderation tables into standalone unique indexes before AutoMigrate inspects the columns.
func migrateModerationUniqueConstraints(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("migrate moderation unique constraints: database is nil")
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}

	for _, target := range moderationUniqueConstraintTargets {
		statement := &gorm.Statement{DB: db}
		if err := statement.Parse(target.model); err != nil {
			return fmt.Errorf("parse %T schema: %w", target.model, err)
		}
		tableName := statement.Schema.Table

		if !db.Migrator().HasTable(target.model) {
			continue
		}

		constraints, err := inspectModerationUniqueConstraints(db, tableName, target.columnName)
		if err != nil {
			return err
		}
		if len(constraints) == 0 {
			continue
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			return migrateModerationTargetUniqueness(tx, target, tableName)
		}); err != nil {
			return err
		}
	}

	return nil
}
