package models

import "time"

// AdobeRegistration stores one Adobe account production job and its evidence.
type AdobeRegistration struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	Email      string `gorm:"size:255;not null;uniqueIndex" json:"email"`
	Password   string `gorm:"size:255" json:"password,omitempty"`
	FirstName  string `gorm:"size:128" json:"first_name"`
	LastName   string `gorm:"size:128" json:"last_name"`
	BirthYear  int    `json:"birth_year"`
	BirthMonth int    `json:"birth_month"`
	Country    string `gorm:"size:8" json:"country"`

	Status string `gorm:"size:32;default:pending;index" json:"status"`
	Note   string `gorm:"type:text" json:"note"`
	Log    string `gorm:"type:text" json:"log,omitempty"`
	Shot   []byte `gorm:"type:blob" json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
