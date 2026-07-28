package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PostPoll is an interactive poll attached to a single post (at most one per post).
type PostPoll struct {
	ID        string    `gorm:"type:text;primary_key" json:"id"`
	PostID    string    `gorm:"type:text;not null;uniqueIndex" json:"postId"`
	Question  string    `gorm:"type:text;not null" json:"question"`
	CreatedAt time.Time `json:"createdAt"`

	Options []PostPollOption `gorm:"foreignKey:PollID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"options,omitempty"`
	Votes   []PostPollVote   `gorm:"foreignKey:PollID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"votes,omitempty"`
}

// BeforeCreate generates UUID and sets timestamp before inserting.
func (p *PostPoll) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	return nil
}

// TableName specifies the table name for PostPoll.
func (PostPoll) TableName() string { return "post_polls" }

// PostPollOption is one selectable choice in a poll.
type PostPollOption struct {
	ID        string `gorm:"type:text;primary_key" json:"id"`
	PollID    string `gorm:"type:text;not null;index" json:"pollId"`
	Text      string `gorm:"type:text;not null" json:"text"`
	SortOrder int    `gorm:"default:0" json:"sortOrder"`

	Votes []PostPollVote `gorm:"foreignKey:OptionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

// BeforeCreate generates UUID before inserting.
func (o *PostPollOption) BeforeCreate(tx *gorm.DB) error {
	if o.ID == "" {
		o.ID = uuid.New().String()
	}
	return nil
}

// TableName specifies the table name for PostPollOption.
func (PostPollOption) TableName() string { return "post_poll_options" }

// PostPollVote records one user's vote. The unique (poll, user) index enforces
// one vote per user; re-voting updates the existing row (changeable vote).
type PostPollVote struct {
	ID        string    `gorm:"type:text;primary_key" json:"id"`
	PollID    string    `gorm:"type:text;not null;index:idx_poll_user,unique" json:"pollId"`
	OptionID  string    `gorm:"type:text;not null;index" json:"optionId"`
	UserID    string    `gorm:"type:text;not null;index:idx_poll_user,unique" json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BeforeCreate generates UUID and sets timestamps before inserting.
func (v *PostPollVote) BeforeCreate(tx *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.New().String()
	}
	now := time.Now()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	return nil
}

// TableName specifies the table name for PostPollVote.
func (PostPollVote) TableName() string { return "post_poll_votes" }
