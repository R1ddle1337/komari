package accounts

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

// CheckPassword 检查密码是否正确
//
// 如果密码正确，返回用户的 UUID 和 true；否则返回空字符串和 false
func CheckPassword(username, passwd string) (uuid string, success bool) {
	uuid, _, success = CheckPasswordWithProof(username, passwd)
	return uuid, success
}

// CheckPasswordWithProof returns the exact stored hash that was verified. A
// password login must pass it to CreatePasswordSession, which rejects a proof
// made stale by a concurrent password reset. The proof must stay server-side.
func CheckPasswordWithProof(username, passwd string) (uuid, passwordProof string, success bool) {
	db := dbcore.GetDBInstance()
	var user models.User
	result := db.Where("username = ?", username).First(&user)
	if result.Error != nil {
		// Keep unknown usernames on the same expensive verification path.
		passwordWork(passwd, make([]byte, passwordSaltBytes))
		return "", "", false
	}
	valid, legacy := verifyPassword(passwd, user.Passwd)
	if !valid {
		return "", "", false
	}
	if legacy {
		hashed, err := hashPassword(passwd)
		if err != nil {
			return "", "", false
		}
		// Do not overwrite a concurrent password reset, or authenticate against
		// the old password after that reset won the race.
		updated := db.Model(&models.User{}).Where("uuid = ? AND passwd = ?", user.UUID, user.Passwd).Update("passwd", hashed)
		if updated.Error != nil {
			return "", "", false
		}
		if updated.RowsAffected == 0 {
			var current models.User
			if db.Where("uuid = ?", user.UUID).First(&current).Error != nil {
				return "", "", false
			}
			if valid, _ := verifyPassword(passwd, current.Passwd); !valid {
				return "", "", false
			}
			user.Passwd = current.Passwd
		} else {
			user.Passwd = hashed
		}
	}
	return user.UUID, user.Passwd, true
}

// ForceResetPassword 强制重置用户密码
func ForceResetPassword(username, passwd string) (err error) {
	db := dbcore.GetDBInstance()
	hashed, err := hashPassword(passwd)
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.User{}).Where("username = ?", username).Update("passwd", hashed)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("无法找到用户名")
		}
		return tx.Where("1 = 1").Delete(&models.Session{}).Error
	})
}

func CreateAccount(username, passwd string) (user models.User, err error) {
	return CreateAccountWithDB(dbcore.GetDBInstance(), username, passwd)
}

func CreateAccountWithDB(db *gorm.DB, username, passwd string) (user models.User, err error) {
	hashedPassword, err := hashPassword(passwd)
	if err != nil {
		return models.User{}, err
	}
	user = models.User{
		UUID:     uuid.New().String(),
		Username: username,
		Passwd:   hashedPassword,
	}
	err = db.Create(&user).Error
	if err != nil {
		return models.User{}, err
	}
	return user, nil
}

func DeleteAccountByUsername(username string) (err error) {
	return DeleteAccountByUsernameWithDB(dbcore.GetDBInstance(), username)
}

func DeleteAccountByUsernameWithDB(db *gorm.DB, username string) (err error) {
	err = db.Where("username = ?", username).Delete(&models.User{}).Error
	if err != nil {
		return err
	}
	return nil
}

func GetUserByUUID(uuid string) (user models.User, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&user).Error
	if err != nil {
		return models.User{}, err
	}
	return user, nil
}

// 通过 SSO 信息获取用户
func GetUserBySSO(ssoID string) (user models.User, err error) {
	db := dbcore.GetDBInstance()

	// 首先尝试查找已存在的用户
	err = db.Where("sso_id = ?", ssoID).First(&user).Error
	if err == nil {
		return user, nil
	}

	// 如果找不到用户，返回明确的错误信息
	return models.User{}, fmt.Errorf("用户不存在：%s", ssoID)
}

func BindingExternalAccount(uuid string, sso_id string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.User{}).Where("uuid = ?", uuid).Update("sso_id", sso_id).Error
	if err != nil {
		return err
	}
	return nil
}

func UnbindExternalAccount(uuid string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.User{}).Where("uuid = ?", uuid).Update("sso_id", "").Error
	if err != nil {
		return err
	}
	return nil
}

func UpdateUser(uuid string, name, password, sso_type *string) error {
	db := dbcore.GetDBInstance()
	updates := make(map[string]interface{})
	if name != nil {
		updates["username"] = *name
	}
	if password != nil {
		hashed, err := hashPassword(*password)
		if err != nil {
			return err
		}
		updates["passwd"] = hashed
	}
	if sso_type != nil {
		updates["sso_type"] = *sso_type
	}
	updates["updated_at"] = time.Now().UTC()
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.User{}).Where("uuid = ?", uuid).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("user not found: %s", uuid)
		}
		if password != nil {
			return tx.Where("1 = 1").Delete(&models.Session{}).Error
		}
		return nil
	})
}
