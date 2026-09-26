package accounts

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	"github.com/komari-monitor/komari/internal/config"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/utils/geoip"
	"github.com/komari-monitor/komari/utils/messageSender"
)

var ErrCredentialsChanged = errors.New("credentials changed; please log in again")

// GetAllSessions 获取所有会话
func GetAllSessions() (sessions []models.Session, err error) {
	db := dbcore.GetDBInstance()
	err = db.Find(&sessions).Error
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

// CreateSession 创建新会话
func CreateSession(uuid string, expires int, userAgent, ip, login_method string) (string, error) {
	db := dbcore.GetDBInstance()
	session := utils.GenerateRandomString(32)

	sessionRecord := models.Session{
		UUID:         uuid,
		Session:      session,
		Expires:      time.Now().UTC().Add(time.Duration(expires) * time.Second),
		UserAgent:    userAgent,
		Ip:           ip,
		LoginMethod:  login_method,
		LatestOnline: time.Now().UTC(),
	}
	err := db.Create(&sessionRecord).Error
	if err != nil {
		return "", err
	}
	notifySessionCreated(userAgent, ip, login_method)
	return session, nil
}

// CreatePasswordSession makes password verification and session creation agree
// even when a password reset commits between them. The conditional INSERT is a
// single database statement; password changes revoke sessions in the same
// transaction as their update, so neither ordering can leave an old login live.
func CreatePasswordSession(uuid, passwordProof string, expires int, userAgent, ip string) (string, error) {
	if uuid == "" || passwordProof == "" {
		return "", ErrCredentialsChanged
	}
	session := utils.GenerateRandomString(32)
	now := time.Now().UTC()
	result := dbcore.GetDBInstance().Exec(`INSERT INTO sessions
		(uuid, session, expires, user_agent, ip, login_method, latest_online, created_at)
		SELECT uuid, ?, ?, ?, ?, ?, ?, ? FROM users WHERE uuid = ? AND passwd = ?`,
		session, now.Add(time.Duration(expires)*time.Second), userAgent, ip, "password", now, now, uuid, passwordProof)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", ErrCredentialsChanged
	}
	notifySessionCreated(userAgent, ip, "password")
	return session, nil
}

func notifySessionCreated(userAgent, ip, loginMethod string) {
	go func() {
		LoginNotification, _ := config.GetAs[bool](config.LoginNotificationKey, false)
		if LoginNotification {
			ipAddr := net.ParseIP(ip)
			ipinfo, _ := geoip.GetGeoInfo(ipAddr)
			loc := "unknown"
			if ipinfo != nil && ipinfo.Name != "" {
				loc = ipinfo.Name
			}
			_ = messageSender.SendNotification(models.EventMessage{
				Event:   messageevent.Login,
				Time:    time.Now().UTC(),
				Message: fmt.Sprintf("%s: %s (%s)\n%s", loginMethod, ip, loc, userAgent),
				Emoji:   "🔑",
			})
		}
	}()
}

// GetSession 根据会话 ID 获取 UUID
func GetSession(session string) (uuid string, err error) {
	db := dbcore.GetDBInstance()
	var sessionRecord models.Session
	// Imported/legacy databases may contain orphan sessions even when newer
	// schemas cascade account deletion. Authentication requires a live owner.
	err = db.Select("sessions.*").Joins("JOIN users ON users.uuid = sessions.uuid").
		Where("sessions.session = ?", session).First(&sessionRecord).Error
	if err != nil {
		return "", err
	}

	if time.Now().UTC().After(sessionRecord.Expires) {
		// 会话已过期，删除它
		_ = DeleteSession(session)
		return "", errors.New("session expired")
	}

	return sessionRecord.UUID, nil
}

func GetUserBySession(session string) (models.User, error) {
	uuid, err := GetSession(session)
	if err != nil {
		return models.User{}, err
	}
	return GetUserByUUID(uuid)
}

// DeleteSession 删除指定会话
func DeleteSession(session string) (err error) {
	db := dbcore.GetDBInstance()
	result := db.Where("session = ?", session).Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func DeleteAllSessions() error {
	db := dbcore.GetDBInstance()
	result := db.Where("1 = 1").Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func UpdateLatest(session, useragent, ip string) error {
	db := dbcore.GetDBInstance()
	return db.Model(&models.Session{}).Where("session = ?", session).Updates(map[string]interface{}{
		"latest_online":     time.Now().UTC(),
		"latest_user_agent": useragent,
		"latest_ip":         ip,
	}).Error
}

func RemoveExpiredSessions() error {
	db := dbcore.GetDBInstance()
	result := db.Where("expires < ?", time.Now().UTC()).Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	return nil
}
