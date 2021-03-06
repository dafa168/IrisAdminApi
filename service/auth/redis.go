package auth

import (
	"github.com/gomodule/redigo/redis"
	"github.com/pkg/errors"
	"github.com/snowlyg/blog/application/libs"
	"github.com/snowlyg/blog/application/libs/logging"
	"github.com/snowlyg/blog/service/cache"
	"strconv"
	"strings"
	"time"
)

type RedisAuth struct {
	Conn *cache.RedisCluster
}

func NewRedisAuth() *RedisAuth {
	return &RedisAuth{
		Conn: cache.GetRedisClusterClient(),
	}
}

// GetAuthId
func (ra *RedisAuth) GetAuthId(token string) (uint, error) {
	sess, err := ra.GetSessionV2(token)
	if err != nil {
		return 0, err
	}
	id := uint(libs.ParseInt(sess.UserId, 10))
	return id, nil
}

//  GetSessionV2 session
func (ra *RedisAuth) GetSessionV2(token string) (*Session, error) {
	sKey := ZxwSessionTokenPrefix + token
	if !ra.Conn.Exists(sKey) {
		return nil, ErrTokenInvalid
	}
	pp := new(Session)
	if err := ra.Conn.LoadRedisHashToStruct(sKey, pp); err != nil {
		return nil, err
	}
	return pp, nil
}

// IsUserTokenOver 超过登录设备限制
func (ra *RedisAuth) IsUserTokenOver(userId string) bool {
	logging.DebugLogger.Debugf("user token count ", ra.getUserTokenCount(userId), " user max count ", ra.getUserTokenMaxCount())
	if ra.getUserTokenCount(userId) >= ra.getUserTokenMaxCount() {
		return true
	}
	return false
}

// getUserTokenCount 获取登录数量
func (ra *RedisAuth) getUserTokenCount(userId string) int {
	count, err := redis.Int(ra.Conn.Scard(ZxwSessionUserPrefix + userId))
	if err != nil {
		logging.ErrorLogger.Errorf("get user token count err: %+v", err)
		return 0
	}
	return count
}

// getUserTokenMaxCount 最大登录限制
func (ra *RedisAuth) getUserTokenMaxCount() int {
	count, err := redis.Int(ra.Conn.GetKey(ZxwSessionUserMaxTokenPrefix))
	if err != nil {
		return ZxwSessionUserMaxTokenDefault
	}
	return count
}

// UserTokenExpired 过期 token
func (ra *RedisAuth) UserTokenExpired(token string) error {
	uKey := ZxwSessionBindUserPrefix + token
	sKeys, err := redis.Strings(ra.Conn.Members(uKey))
	if err != nil {
		logging.ErrorLogger.Errorf("user token expired get members err: %+v", err)
		return err
	}
	for _, v := range sKeys {
		if !strings.Contains(v, ZxwSessionUserPrefix) {
			continue
		}
		_, err = ra.Conn.Do("SREM", v, token)
		if err != nil {
			logging.ErrorLogger.Errorf("user token expired do srem err: %+v", err)
			return err
		}
	}
	if _, err = ra.Conn.Del(uKey); err != nil {
		logging.ErrorLogger.Errorf("user token expired del err: %+v", err)
		return err
	}
	return nil
}

// getUserScope 角色
func GetUserScope(userType string) uint64 {
	switch userType {
	case "admin":
		return AdminScope
	}
	return NoneScope
}

// ToCache 缓存 token
func (ra *RedisAuth) ToCache(token string, id uint64) error {
	sKey := ZxwSessionTokenPrefix + token
	rsv2 := &Session{
		UserId:       strconv.FormatUint(id, 10),
		LoginType:    LoginTypeWeb,
		AuthType:     AuthPwd,
		CreationDate: time.Now().Unix(),
		Scope:        GetUserScope("admin"),
	}
	if _, err := ra.Conn.HMSet(sKey,
		"user_id", rsv2.UserId,
		"login_type", rsv2.LoginType,
		"auth_type", rsv2.AuthType,
		"creation_data", rsv2.CreationDate,
		"expires_in", rsv2.ExpiresIn,
		"scope", rsv2.Scope,
	); err != nil {
		logging.ErrorLogger.Errorf("to cache token err: %+v", err)
		return err
	}

	return nil
}

// SyncUserTokenCache 同步 token 到缓存
func (ra *RedisAuth) SyncUserTokenCache(token string) error {
	rsv2, err := ra.GetSessionV2(token)
	if err != nil {
		return err
	}
	sKey := ZxwSessionUserPrefix + rsv2.UserId
	if _, err := ra.Conn.Sadd(sKey, token); err != nil {
		logging.ErrorLogger.Errorf("sync user token cache sadd err: %+v", err)
		return err
	}
	sKey2 := ZxwSessionBindUserPrefix + token
	_, err = ra.Conn.Sadd(sKey2, sKey)
	if err != nil {
		logging.ErrorLogger.Errorf("sync user token cache sadd err: %+v", err)
		return err
	}
	return nil
}

//UpdateUserTokenCacheExpire 更新过期时间
func (ra *RedisAuth) UpdateUserTokenCacheExpire(token string) error {
	rsv2, err := ra.GetSessionV2(token)
	if err != nil {
		return err
	}
	if rsv2 == nil {
		return errors.New("token cache is nil")
	}
	if _, err = ra.Conn.Expire(ZxwSessionTokenPrefix+token, int(ra.getTokenExpire(rsv2).Seconds())); err != nil {
		logging.ErrorLogger.Errorf("update user token cache expire err: %+v", err)
		return err
	}
	return nil
}

// getTokenExpire 过期时间
func (ra *RedisAuth) getTokenExpire(rsv2 *Session) time.Duration {
	timeout := RedisSessionTimeoutApp
	if rsv2.LoginType == LoginTypeWeb {
		timeout = RedisSessionTimeoutWeb
	} else if rsv2.LoginType == LoginTypeWx {
		timeout = RedisSessionTimeoutWx
	} else if rsv2.LoginType == LoginTypeAlipay {
		timeout = RedisSessionTimeoutWx
	}
	return timeout
}

// DelUserTokenCache 删除token缓存
func (ra *RedisAuth) DelUserTokenCache(token string) error {
	rsv2, err := ra.GetSessionV2(token)
	if err != nil {
		return err
	}
	if rsv2 == nil {
		return errors.New("token cache is nil")
	}
	sKey := ZxwSessionUserPrefix + rsv2.UserId
	_, err = ra.Conn.Do("SREM", sKey, token)
	if err != nil {
		logging.ErrorLogger.Errorf("del user token cache do srem err: %+v", err)
		return err
	}
	err = ra.DelTokenCache(token)
	if err != nil {
		return err
	}

	return nil
}

// DelTokenCache 删除token缓存
func (ra *RedisAuth) DelTokenCache(token string) error {
	sKey2 := ZxwSessionBindUserPrefix + token
	_, err := ra.Conn.Del(sKey2)
	if err != nil {
		logging.ErrorLogger.Errorf("del token cache del key err: %+v", err)
		return err
	}

	sKey3 := ZxwSessionTokenPrefix + token
	_, err = ra.Conn.Del(sKey3)
	if err != nil {
		logging.ErrorLogger.Errorf("del token cache del key err: %+v", err)
		return err
	}

	return nil
}

// CleanUserTokenCache 清空token缓存
func (ra *RedisAuth) CleanUserTokenCache(token string) error {
	rsv2, err := ra.GetSessionV2(token)
	if err != nil {
		logging.ErrorLogger.Errorf("clean user token cache member err: %+v", err)
		return err
	}
	sKey := ZxwSessionUserPrefix + rsv2.UserId
	var allTokens []string
	allTokens, err = redis.Strings(ra.Conn.Members(sKey))
	if err != nil {
		logging.ErrorLogger.Errorf("clean user token cache member err: %+v", err)
		return err
	}
	_, err = ra.Conn.Del(sKey)
	if err != nil {
		logging.ErrorLogger.Errorf("clean user token cache del err: %+v", err)
		return err
	}

	for _, token := range allTokens {
		err = ra.DelTokenCache(token)
		if err != nil {
			return err
		}
	}
	return nil
}

// Close
func (ra *RedisAuth) Close() {
	ra.Conn.Close()
}
