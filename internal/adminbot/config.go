package adminbot

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
)

// Config is one operator's management bot as the rest of the panel works
// with it: the model row, in the shape the manager and the panel's page
// pass around.
type Config struct {
	OperatorID  uint
	Enabled     bool
	Token       string
	AdminChatID int64
}

func configFromRow(r *model.AdminBot) *Config {
	return &Config{
		OperatorID:  r.OperatorID,
		Enabled:     r.Enabled,
		Token:       r.Token,
		AdminChatID: r.AdminChatID,
	}
}

func (c *Config) row() *model.AdminBot {
	return &model.AdminBot{
		OperatorID:  c.OperatorID,
		Enabled:     c.Enabled,
		Token:       c.Token,
		AdminChatID: c.AdminChatID,
	}
}

// ConfigFor reads one operator's management-bot configuration. An
// operator who has never set one up gets the zero Config -- disabled,
// no token -- rather than an error: "no bot yet" is the ordinary state
// of most operators, and every caller would only translate the error
// back into that. It is a method on the Manager because the manager is
// the one holder of this store: the panel's page asks it, and its own
// Start and Reload read through the same door.
func (m *Manager) ConfigFor(ctx context.Context, operatorID uint) (*Config, error) {
	var row model.AdminBot
	err := m.db.WithContext(ctx).Where("operator_id = ?", operatorID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &Config{OperatorID: operatorID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("adminbot: read bot config: %w", err)
	}
	return configFromRow(&row), nil
}

// SaveConfig writes an operator's management-bot configuration, creating
// the row the first time and replacing it after that. One row per
// operator is the table's own constraint; this is the upsert that
// honours it.
func (m *Manager) SaveConfig(ctx context.Context, c *Config) error {
	if c.OperatorID == 0 {
		return fault.Field("operatorId", "a bot belongs to an operator")
	}
	row := c.row()
	db := m.db.WithContext(ctx)
	var existing model.AdminBot
	err := db.Where("operator_id = ?", c.OperatorID).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		if err := db.Create(row).Error; err != nil {
			return fmt.Errorf("adminbot: save bot config: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("adminbot: read bot config: %w", err)
	}
	row.ID = existing.ID
	row.CreatedAt = existing.CreatedAt
	if err := db.Save(row).Error; err != nil {
		return fmt.Errorf("adminbot: save bot config: %w", err)
	}
	return nil
}

// configs reads every operator's management-bot configuration, for the
// manager starting the fleet: which bots exist and are switched on is a
// question about all of them at once.
func (m *Manager) configs(ctx context.Context) ([]Config, error) {
	var rows []model.AdminBot
	if err := m.db.WithContext(ctx).Order("operator_id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("adminbot: read bot configs: %w", err)
	}
	out := make([]Config, 0, len(rows))
	for i := range rows {
		out = append(out, *configFromRow(&rows[i]))
	}
	return out, nil
}
