package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/user-service/models"
)

const maxStatusBytes = 512

func (s *UserService) GetStatusByName(c *natsrouter.Context, req models.StatusGetByNameRequest) (*models.StatusView, error) {
	c.WithLogValues("account", c.Param("account"), "target", req.Name)
	u, err := s.users.GetUserStatus(c, req.Name)
	if err != nil {
		return nil, fmt.Errorf("get status: %w", err)
	}
	if u == nil {
		return nil, errcode.NotFound("user not found")
	}
	return &models.StatusView{
		Account:      u.Account,
		StatusText:   u.StatusText,
		StatusIsShow: u.StatusIsShow,
		ChineseName:  u.ChineseName,
		EngName:      u.EngName,
	}, nil
}

// GetProfileByName is an intentional twin of GetStatusByName — same request,
// same response fields, same users-collection query; clients use it as the
// profile lookup. This endpoint should fetch data from the HR collection
// before querying the Mongo users collection for statusIsShow and statusText;
// not implemented here as it needs to be done in the internal repo.
func (s *UserService) GetProfileByName(c *natsrouter.Context, req models.StatusGetByNameRequest) (*models.StatusView, error) {
	return s.GetStatusByName(c, req)
}

func (s *UserService) SetStatus(c *natsrouter.Context, req models.StatusSetRequest) (*models.StatusView, error) {
	account := c.Param("account")
	c.WithLogValues("account", account)
	if len(req.Text) > maxStatusBytes {
		return nil, errcode.BadRequest("status text too long")
	}
	matched, err := s.users.SetUserStatus(c, account, req.Text, req.IsShow)
	if err != nil {
		return nil, fmt.Errorf("set status: %w", err)
	}
	if !matched {
		// No active user doc matched — don't broadcast a status nobody owns.
		return nil, errcode.NotFound("user not found")
	}
	s.publishStatus(c, account, req.Text, req.IsShow)
	return s.GetStatusByName(c, models.StatusGetByNameRequest{Name: account})
}

// publishStatus broadcasts via core NATS to every configured site except self; errors are logged, not returned.
func (s *UserService) publishStatus(c *natsrouter.Context, account, text string, isShow *bool) {
	evt := model.UserStatusUpdated{
		Account:      account,
		StatusText:   text,
		StatusIsShow: isShow,
		Timestamp:    time.Now().UTC().UnixMilli(),
	}
	data, _ := json.Marshal(evt) // UserStatusUpdated is all primitives — Marshal cannot fail
	for _, dest := range s.allSiteIDs {
		if dest == "" || dest == s.siteID {
			continue
		}
		if err := s.pub.Publish(c, subject.Outbox(s.siteID, dest, model.OutboxUserStatusUpdated), data); err != nil {
			slog.ErrorContext(c, "publish status outbox", "error", err, "site", s.siteID, "dest", dest, "account", account, "request_id", natsutil.RequestIDFromContext(c))
		}
	}
}
