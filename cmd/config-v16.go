/*
 * Minio Cloud Storage, (C) 2016, 2017 Minio, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cmd

import (
	"errors"
	"fmt"
	"io/ioutil"
	"strings"
	"sync"

	"github.com/minio/minio/pkg/quick"
	"github.com/tidwall/gjson"
)

// Read Write mutex for safe access to ServerConfig.
var serverConfigMu sync.RWMutex

var v16 = "16"

// serverConfigV16 server configuration version '16' which is like
// version '15' except it removes log level field and renames `fileName`
// field of File logger to `filename`
type serverConfigV16 struct {
	Version string `json:"version"`

	// S3 API configuration.
	Credential credential `json:"credential"`
	Region     string     `json:"region"`
	Browser    string     `json:"browser"`

	// Additional error logging configuration.
	Logger *loggers `json:"logger"`

	// Notification queue configuration.
	Notify *notifier `json:"notify"`
}

func newServerConfigV16() *serverConfigV16 {
	srvCfg := &serverConfigV16{
		Version: v16,
		Region:  globalMinioDefaultRegion,
		Logger:  &loggers{},
		Notify:  &notifier{},
	}
	srvCfg.SetCredential(mustGetNewCredential())
	srvCfg.SetBrowser("on")
	// Enable console logger by default on a fresh run.
	srvCfg.Logger.Console = NewConsoleLogger()

	// Make sure to initialize notification configs.
	srvCfg.Notify.AMQP = make(map[string]amqpNotify)
	srvCfg.Notify.AMQP["1"] = amqpNotify{}
	srvCfg.Notify.ElasticSearch = make(map[string]elasticSearchNotify)
	srvCfg.Notify.ElasticSearch["1"] = elasticSearchNotify{}
	srvCfg.Notify.Redis = make(map[string]redisNotify)
	srvCfg.Notify.Redis["1"] = redisNotify{}
	srvCfg.Notify.NATS = make(map[string]natsNotify)
	srvCfg.Notify.NATS["1"] = natsNotify{}
	srvCfg.Notify.PostgreSQL = make(map[string]postgreSQLNotify)
	srvCfg.Notify.PostgreSQL["1"] = postgreSQLNotify{}
	srvCfg.Notify.MySQL = make(map[string]mySQLNotify)
	srvCfg.Notify.MySQL["1"] = mySQLNotify{}
	srvCfg.Notify.Kafka = make(map[string]kafkaNotify)
	srvCfg.Notify.Kafka["1"] = kafkaNotify{}
	srvCfg.Notify.Webhook = make(map[string]webhookNotify)
	srvCfg.Notify.Webhook["1"] = webhookNotify{}

	return srvCfg
}

// newConfig - initialize a new server config, saves env parameters if
// found, otherwise use default parameters
func newConfig(envParams envParams) error {
	// Initialize server config.
	srvCfg := newServerConfigV16()

	// If env is set for a fresh start, save them to config file.
	if globalIsEnvCreds {
		srvCfg.SetCredential(envParams.creds)
	}

	if globalIsEnvBrowser {
		srvCfg.SetBrowser(envParams.browser)
	}

	// Create config path.
	if err := createConfigDir(); err != nil {
		return err
	}

	// hold the mutex lock before a new config is assigned.
	// Save the new config globally.
	// unlock the mutex.
	serverConfigMu.Lock()
	serverConfig = srvCfg
	serverConfigMu.Unlock()

	// Save config into file.
	return serverConfig.Save()
}

// loadConfig - loads a new config from disk, overrides params from env
// if found and valid
func loadConfig(envParams envParams) error {
	configFile := getConfigFile()

	srvCfg := &serverConfigV16{}
	if _, err := quick.Load(configFile, srvCfg); err != nil {
		return err
	}
	if srvCfg.Version != v16 {
		return fmt.Errorf("configuration version mismatch.  Expected: ‘%s’, Got: ‘%s’", srvCfg.Version, v16)
	}

	// If env is set override the credentials from config file.
	if globalIsEnvCreds {
		srvCfg.SetCredential(envParams.creds)
	}

	if globalIsEnvBrowser {
		srvCfg.SetBrowser(envParams.browser)
	}

	if strings.ToLower(srvCfg.GetBrowser()) == "off" {
		globalIsBrowserEnabled = false
	}

	// hold the mutex lock before a new config is assigned.
	serverConfigMu.Lock()
	// Save the loaded config globally.
	serverConfig = srvCfg
	serverConfigMu.Unlock()
	return nil
}

// doCheckDupJSONKeys recursively detects duplicate json keys
func doCheckDupJSONKeys(key, value gjson.Result) error {
	// Key occurrences map of the current scope to count
	// if there is any duplicated json key.
	keysOcc := make(map[string]int)

	// Holds the found error
	var checkErr error

	// Iterate over keys in the current json scope
	value.ForEach(func(k, v gjson.Result) bool {
		// If current key is not null, check if its
		// value contains some duplicated keys.
		if k.Type != gjson.Null {
			keysOcc[k.String()]++
			checkErr = doCheckDupJSONKeys(k, v)
		}
		return checkErr == nil
	})

	// Check found err
	if checkErr != nil {
		return errors.New(key.String() + " => " + checkErr.Error())
	}

	// Check for duplicated keys
	for k, v := range keysOcc {
		if v > 1 {
			return errors.New(key.String() + " => `" + k + "` entry is duplicated")
		}
	}

	return nil
}

// Check recursively if a key is duplicated in the same json scope
// e.g.:
//  `{ "key" : { "key" ..` is accepted
//  `{ "key" : { "subkey" : "val1", "subkey": "val2" ..` throws subkey duplicated error
func checkDupJSONKeys(json string) error {
	// Parse config with gjson library
	config := gjson.Parse(json)

	// Create a fake rootKey since root json doesn't seem to have representation
	// in gjson library.
	rootKey := gjson.Result{Type: gjson.String, Str: minioConfigFile}

	// Check if loaded json contains any duplicated keys
	return doCheckDupJSONKeys(rootKey, config)
}

// validateConfig checks for
func validateConfig() error {
	configFile := getConfigFile()

	srvCfg := &serverConfigV16{}
	if _, err := quick.Load(configFile, srvCfg); err != nil {
		return err
	}

	// Check if config version is valid
	if srvCfg.Version != v16 {
		return errors.New("bad config version, expected: " + v16)
	}

	// Load config file json and check for duplication json keys
	jsonBytes, err := ioutil.ReadFile(configFile)
	if err != nil {
		return err
	}
	if err := checkDupJSONKeys(string(jsonBytes)); err != nil {
		return err
	}

	// Validate region field
	if srvCfg.GetRegion() == "" {
		return errors.New("Region config value cannot be empty")
	}

	// Validate browser field
	if b := strings.ToLower(srvCfg.GetBrowser()); b != "on" && b != "off" {
		return fmt.Errorf("Browser config value %s is invalid", b)
	}

	// Validate credential fields only when
	// they are not set via the environment
	if !globalIsEnvCreds {
		if !srvCfg.Credential.IsValid() {
			return errors.New("invalid credential")
		}
	}

	// Validate logger field
	if err := srvCfg.Logger.Validate(); err != nil {
		return err
	}

	// Validate notify field
	if err := srvCfg.Notify.Validate(); err != nil {
		return err
	}

	return nil
}

// serverConfig server config.
var serverConfig *serverConfigV16

// GetVersion get current config version.
func (s serverConfigV16) GetVersion() string {
	serverConfigMu.RLock()
	defer serverConfigMu.RUnlock()

	return s.Version
}

// SetRegion set new region.
func (s *serverConfigV16) SetRegion(region string) {
	serverConfigMu.Lock()
	defer serverConfigMu.Unlock()

	// Empty region means "us-east-1" by default from S3 spec.
	if region == "" {
		region = "us-east-1"
	}
	s.Region = region
}

// GetRegion get current region.
func (s serverConfigV16) GetRegion() string {
	serverConfigMu.RLock()
	defer serverConfigMu.RUnlock()

	if s.Region != "" {
		return s.Region
	} // region empty

	// Empty region means "us-east-1" by default from S3 spec.
	return "us-east-1"
}

// SetCredentials set new credentials.
func (s *serverConfigV16) SetCredential(creds credential) {
	serverConfigMu.Lock()
	defer serverConfigMu.Unlock()

	// Set updated credential.
	s.Credential = creds
}

// GetCredentials get current credentials.
func (s serverConfigV16) GetCredential() credential {
	serverConfigMu.RLock()
	defer serverConfigMu.RUnlock()

	return s.Credential
}

// SetBrowser set if browser is enabled.
func (s *serverConfigV16) SetBrowser(v string) {
	serverConfigMu.Lock()
	defer serverConfigMu.Unlock()

	// Set browser param
	if v == "" {
		v = "on" // Browser is on by default.
	}

	// Set the new value.
	s.Browser = v
}

// GetCredentials get current credentials.
func (s serverConfigV16) GetBrowser() string {
	serverConfigMu.RLock()
	defer serverConfigMu.RUnlock()

	if s.Browser != "" {
		return s.Browser
	} // empty browser.

	// Empty browser means "on" by default.
	return "on"
}

// Save config.
func (s serverConfigV16) Save() error {
	serverConfigMu.RLock()
	defer serverConfigMu.RUnlock()

	// get config file.
	configFile := getConfigFile()

	// Save config file.
	return quick.Save(configFile, &s)
}
