// Command switchboard-admin is the admin CLI. It manages users in the
// global database, orgs (router config + per-org DB bootstrap), and the
// per-org providers and upstream models; keys, quotas and aliases come
// later.
package main

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
	_ "modernc.org/sqlite"

	"github.com/jrsap/switchboard/internal/config"
	"github.com/jrsap/switchboard/internal/database"
	"github.com/jrsap/switchboard/internal/globaldb"
	"github.com/jrsap/switchboard/internal/orgdb"
	"github.com/jrsap/switchboard/internal/orgs"
	"github.com/jrsap/switchboard/internal/seal"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	defaultConfig := os.Getenv("SWITCHBOARD_CONFIG")
	if defaultConfig == "" {
		defaultConfig = "config.json"
	}
	dbPath := flag.String("db", os.Getenv("SWITCHBOARD_GLOBAL_DB"), "path to the global database (flag > SWITCHBOARD_GLOBAL_DB > config global_db > ./switchboard-global.db)")
	configPath := flag.String("config", defaultConfig, "path to the router config (or SWITCHBOARD_CONFIG)")
	dataDir := flag.String("data-dir", os.Getenv("SWITCHBOARD_DATA_DIR"), "base directory for per-org storage (flag > SWITCHBOARD_DATA_DIR > config data_dir > ./data)")
	flag.Usage = usage
	flag.Parse()

	// Fill unset -db/-data-dir from the config file (best effort: user
	// commands don't need the config, org commands load it again anyway).
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if !explicit["db"] && *dbPath == "" || !explicit["data-dir"] && *dataDir == "" {
		if cfg, err := config.Load(*configPath); err == nil {
			if !explicit["db"] && *dbPath == "" && cfg.GlobalDB != "" {
				*dbPath = cfg.GlobalDB
			}
			if !explicit["data-dir"] && *dataDir == "" && cfg.DataDir != "" {
				*dataDir = cfg.DataDir
			}
		}
	}
	if *dbPath == "" {
		*dbPath = "switchboard-global.db"
	}
	if *dataDir == "" {
		*dataDir = "data"
	}

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "user":
		err = userCommand(*dbPath, args[1:])
	case "org":
		err = orgCommand(*dbPath, *configPath, *dataDir, args[1:])
	case "provider":
		err = providerCommand(*configPath, args[1:])
	case "model":
		err = modelCommand(*configPath, args[1:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "switchboard-admin: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: switchboard-admin [-db PATH] [-config PATH] [-data-dir DIR] <command> [flags]

Commands:
  user add -username NAME [-password PASS] [-org ORG]...
  user list
  user show -username NAME
  user delete -username NAME
  user set-password -username NAME [-password PASS]
  user set-orgs -username NAME [-org ORG]...
  org add -id ID -name NAME -admin-username NAME [-admin-password PASS] [-child-addr host:port]
  org list
  provider add -org ORG -name NAME -base-url URL [-api-key KEY]
  provider list -org ORG
  provider delete -org ORG -name NAME
  model add -org ORG -provider NAME -name NAME [-tool-calling] [-vision] [-reasoning-level LEVEL]...
  model list -org ORG [-provider NAME]
  model delete -org ORG -provider NAME -name NAME

The provider/model commands operate on the per-org database located via the router
config (storage.db of the org entry). Provider API keys are stored sealed with the
org data key; -tool-calling / -vision are manual checkboxes and -reasoning-level is
repeatable (the model's supported reasoning levels).

The router config defaults to SWITCHBOARD_CONFIG or ./config.json. The global database
path and per-org data dir resolve as: flag > env (SWITCHBOARD_GLOBAL_DB /
SWITCHBOARD_DATA_DIR) > config (global_db / data_dir) > ./switchboard-global.db, ./data.
If -password/-admin-password is omitted, it is prompted for.
`)
}

func orgCommand(dbPath, configPath, dataDir string, args []string) error {
	if len(args) == 0 {
		return errors.New("org: missing subcommand (add, list)")
	}
	switch args[0] {
	case "add":
		return orgAdd(dbPath, configPath, dataDir, args[1:])
	case "list":
		return orgList(configPath, args[1:])
	default:
		return fmt.Errorf("org: unknown subcommand %q", args[0])
	}
}

func orgAdd(dbPath, configPath, dataDir string, args []string) error {
	fs := flag.NewFlagSet("org add", flag.ExitOnError)
	id := fs.String("id", "", "org id (lowercase [a-z0-9-], also the API key prefix) (required)")
	name := fs.String("name", "", "org name (required)")
	adminUsername := fs.String("admin-username", "", "username of the admin created with the org (required)")
	adminPassword := fs.String("admin-password", "", "password for the admin user if it must be created (prompted if omitted)")
	childAddr := fs.String("child-addr", "", "loopback host:port for the org child (allocated if omitted)")
	fs.Parse(args)

	if *id == "" || *name == "" || *adminUsername == "" || !usernameRe.MatchString(*adminUsername) {
		return errors.New("org add: -id, -name and a valid -admin-username are required")
	}

	rootKey, err := orgs.RootKeyFromConfig(configPath)
	if err != nil {
		return err
	}
	gdb, err := openGlobalDB(dbPath)
	if err != nil {
		return err
	}
	defer gdb.Close()

	// Only prompt for a password if the global user does not exist yet.
	var hash string
	needPassword := false
	if _, err := gdb.GetUserByUsername(*adminUsername); errors.Is(err, globaldb.ErrNotFound) {
		needPassword = true
	} else if err != nil {
		return err
	}
	if needPassword {
		pass, err := passwordValue(*adminPassword)
		if err != nil {
			return err
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		hash = string(h)
	}

	org, err := orgs.Create(orgs.CreateOptions{
		ConfigPath:        configPath,
		DataDir:           dataDir,
		ID:                *id,
		Name:              *name,
		ChildAddr:         *childAddr,
		AdminUsername:     *adminUsername,
		AdminPasswordHash: hash,
		RootKey:           rootKey,
		GlobalDB:          gdb,
		OpenSQL:           openOrgSQL,
	})
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(org.Storage.DB)
	fmt.Printf("created org %q (id %s)\n  name:        %s\n  child_addr:  %s\n  org db:      %s\n  archive:     %s\n  admin user:  %s\nrestart the router to spawn its child process\n",
		org.Name, org.ID, org.Name, org.ChildAddr, abs, org.Storage.ArchiveRoot, *adminUsername)
	return nil
}

func orgList(configPath string, args []string) error {
	fs := flag.NewFlagSet("org list", flag.ExitOnError)
	fs.Parse(args)
	orgsList, err := orgs.List(configPath)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tCHILD_ADDR\tORG DB")
	for _, o := range orgsList {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", o.ID, o.Name, o.ChildAddr, o.Storage.DB)
	}
	return w.Flush()
}

func openOrgSQL(path string) (*sql.DB, error) {
	return sql.Open("sqlite", database.SQLiteDSN(path))
}

// openOrg loads the router config, locates the org, opens its per-org DB and
// unwraps the org data key (root key from the config unwrapping the config
// entry's wrapped key, cross-checked against the org DB meta). The returned
// data key seals upstream provider API keys at rest.
func openOrg(configPath, orgID string) (*orgdb.DB, []byte, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	org := cfg.Org(orgID)
	if org == nil {
		return nil, nil, fmt.Errorf("org %q not found in %s", orgID, configPath)
	}
	rootKey, err := base64.StdEncoding.DecodeString(cfg.RootKey)
	if err != nil || len(rootKey) != seal.KeySize {
		return nil, nil, fmt.Errorf("root_key must be a base64-encoded %d-byte key", seal.KeySize)
	}
	dataKey, err := seal.Unwrap(rootKey, org.WrappedDataKey)
	if err != nil {
		return nil, nil, fmt.Errorf("unwrap data key of org %q: %w", orgID, err)
	}
	sqlDB, err := openOrgSQL(org.Storage.DB)
	if err != nil {
		return nil, nil, err
	}
	odb, err := orgdb.Open(sqlDB)
	if err != nil {
		sqlDB.Close()
		return nil, nil, err
	}
	wrapped, err := odb.GetMeta(orgdb.MetaWrappedDataKey)
	if err != nil || wrapped != org.WrappedDataKey {
		odb.Close()
		return nil, nil, fmt.Errorf("org %q: wrapped data key in the org DB does not match the router config", orgID)
	}
	return odb, dataKey, nil
}

func keyStatus(sealed string) string {
	if sealed == "" {
		return "none"
	}
	return "set (sealed at rest)"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func providerCommand(configPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("provider: missing subcommand (add, list, delete)")
	}
	switch args[0] {
	case "add":
		return providerAdd(configPath, args[1:])
	case "list":
		return providerList(configPath, args[1:])
	case "delete":
		return providerDelete(configPath, args[1:])
	default:
		return fmt.Errorf("provider: unknown subcommand %q", args[0])
	}
}

func providerAdd(configPath string, args []string) error {
	fs := flag.NewFlagSet("provider add", flag.ExitOnError)
	orgID := fs.String("org", "", "org id (required)")
	name := fs.String("name", "", "provider name (required)")
	baseURL := fs.String("base-url", "", "upstream base URL, e.g. https://api.openai.com/v1 (required)")
	apiKey := fs.String("api-key", "", "upstream API key, stored sealed with the org data key (empty = no key)")
	fs.Parse(args)
	if *orgID == "" || *name == "" || *baseURL == "" {
		return errors.New("provider add: -org, -name and -base-url are required")
	}
	odb, dataKey, err := openOrg(configPath, *orgID)
	if err != nil {
		return err
	}
	defer odb.Close()
	sealed := ""
	if *apiKey != "" {
		if sealed, err = seal.Encrypt(dataKey, []byte(*apiKey)); err != nil {
			return err
		}
	}
	p, err := odb.AddProvider(*name, *baseURL, sealed)
	if err != nil {
		return err
	}
	fmt.Printf("created provider %q (id %d)\n  base_url: %s\n  api_key:  %s\n", p.Name, p.ID, p.BaseURL, keyStatus(sealed))
	return nil
}

func providerList(configPath string, args []string) error {
	fs := flag.NewFlagSet("provider list", flag.ExitOnError)
	orgID := fs.String("org", "", "org id (required)")
	fs.Parse(args)
	if *orgID == "" {
		return errors.New("provider list: -org is required")
	}
	odb, _, err := openOrg(configPath, *orgID)
	if err != nil {
		return err
	}
	defer odb.Close()
	providers, err := odb.ListProviders()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tBASE_URL\tAPI_KEY\tCREATED")
	for _, p := range providers {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.BaseURL, keyStatus(p.APIKey), p.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	return w.Flush()
}

func providerDelete(configPath string, args []string) error {
	fs := flag.NewFlagSet("provider delete", flag.ExitOnError)
	orgID := fs.String("org", "", "org id (required)")
	name := fs.String("name", "", "provider name (required)")
	fs.Parse(args)
	if *orgID == "" || *name == "" {
		return errors.New("provider delete: -org and -name are required")
	}
	odb, _, err := openOrg(configPath, *orgID)
	if err != nil {
		return err
	}
	defer odb.Close()
	p, err := odb.GetProviderByName(*name)
	if errors.Is(err, orgdb.ErrNotFound) {
		return fmt.Errorf("provider %q not found in org %q", *name, *orgID)
	}
	if err != nil {
		return err
	}
	if err := odb.DeleteProvider(p.ID); err != nil {
		return err
	}
	fmt.Printf("deleted provider %q (id %d) and its models\n", p.Name, p.ID)
	return nil
}

func modelCommand(configPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("model: missing subcommand (add, list, delete)")
	}
	switch args[0] {
	case "add":
		return modelAdd(configPath, args[1:])
	case "list":
		return modelList(configPath, args[1:])
	case "delete":
		return modelDelete(configPath, args[1:])
	default:
		return fmt.Errorf("model: unknown subcommand %q", args[0])
	}
}

func modelAdd(configPath string, args []string) error {
	fs := flag.NewFlagSet("model add", flag.ExitOnError)
	orgID := fs.String("org", "", "org id (required)")
	providerName := fs.String("provider", "", "provider name (required)")
	name := fs.String("name", "", "upstream model name, e.g. gpt-4o (required)")
	toolCalling := fs.Bool("tool-calling", false, "check if the model supports tool calling")
	vision := fs.Bool("vision", false, "check if the model supports vision input")
	var levels stringSlice
	fs.Var(&levels, "reasoning-level", "supported reasoning level, repeatable (replaces nothing, sets the list)")
	fs.Parse(args)
	if *orgID == "" || *providerName == "" || *name == "" {
		return errors.New("model add: -org, -provider and -name are required")
	}
	odb, _, err := openOrg(configPath, *orgID)
	if err != nil {
		return err
	}
	defer odb.Close()
	p, err := odb.GetProviderByName(*providerName)
	if errors.Is(err, orgdb.ErrNotFound) {
		return fmt.Errorf("provider %q not found in org %q", *providerName, *orgID)
	}
	if err != nil {
		return err
	}
	m, err := odb.AddModel(p.ID, *name, *toolCalling, *vision, levels)
	if err != nil {
		return err
	}
	fmt.Printf("created model %q (id %d) on provider %q\n  tool_calling:     %s\n  vision:           %s\n  reasoning_levels: %s\n",
		m.Name, m.ID, m.ProviderName, yesNo(m.ToolCalling), yesNo(m.Vision), strings.Join(m.ReasoningLevels, ","))
	return nil
}

func modelList(configPath string, args []string) error {
	fs := flag.NewFlagSet("model list", flag.ExitOnError)
	orgID := fs.String("org", "", "org id (required)")
	providerName := fs.String("provider", "", "limit to one provider")
	fs.Parse(args)
	if *orgID == "" {
		return errors.New("model list: -org is required")
	}
	odb, _, err := openOrg(configPath, *orgID)
	if err != nil {
		return err
	}
	defer odb.Close()
	var models []*orgdb.Model
	if *providerName != "" {
		p, err := odb.GetProviderByName(*providerName)
		if errors.Is(err, orgdb.ErrNotFound) {
			return fmt.Errorf("provider %q not found in org %q", *providerName, *orgID)
		}
		if err != nil {
			return err
		}
		models, err = odb.ListModelsByProvider(p.ID)
		if err != nil {
			return err
		}
	} else {
		models, err = odb.ListModels()
		if err != nil {
			return err
		}
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPROVIDER\tMODEL\tTOOL_CALLING\tVISION\tREASONING_LEVELS\tCREATED")
	for _, m := range models {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", m.ID, m.ProviderName, m.Name, yesNo(m.ToolCalling), yesNo(m.Vision), strings.Join(m.ReasoningLevels, ","), m.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	return w.Flush()
}

func modelDelete(configPath string, args []string) error {
	fs := flag.NewFlagSet("model delete", flag.ExitOnError)
	orgID := fs.String("org", "", "org id (required)")
	providerName := fs.String("provider", "", "provider name (required)")
	name := fs.String("name", "", "upstream model name (required)")
	fs.Parse(args)
	if *orgID == "" || *providerName == "" || *name == "" {
		return errors.New("model delete: -org, -provider and -name are required")
	}
	odb, _, err := openOrg(configPath, *orgID)
	if err != nil {
		return err
	}
	defer odb.Close()
	p, err := odb.GetProviderByName(*providerName)
	if errors.Is(err, orgdb.ErrNotFound) {
		return fmt.Errorf("provider %q not found in org %q", *providerName, *orgID)
	}
	if err != nil {
		return err
	}
	if err := odb.DeleteModel(p.ID, *name); err != nil {
		if errors.Is(err, orgdb.ErrNotFound) {
			return fmt.Errorf("model %q not found on provider %q", *name, *providerName)
		}
		return err
	}
	fmt.Printf("deleted model %q from provider %q\n", *name, *providerName)
	return nil
}

func userCommand(dbPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("user: missing subcommand (add, list, show, delete, set-password, set-orgs)")
	}
	db, err := openGlobalDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	switch args[0] {
	case "add":
		return userAdd(db, args[1:])
	case "list":
		return userList(db)
	case "show":
		return userShow(db, args[1:])
	case "delete":
		return userDelete(db, args[1:])
	case "set-password":
		return userSetPassword(db, args[1:])
	case "set-orgs":
		return userSetOrgs(db, args[1:])
	default:
		return fmt.Errorf("user: unknown subcommand %q", args[0])
	}
}

func openGlobalDB(path string) (*globaldb.DB, error) {
	sqlDB, err := sql.Open("sqlite", database.SQLiteDSN(path))
	if err != nil {
		return nil, err
	}
	return globaldb.Open(sqlDB)
}

func userAdd(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user add", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	password := fs.String("password", "", "password (prompted if omitted)")
	var orgs stringSlice
	fs.Var(&orgs, "org", "org membership (routing cache), repeatable")
	fs.Parse(args)

	if *username == "" || !usernameRe.MatchString(*username) {
		return fmt.Errorf("invalid username %q: 1-64 chars of [a-zA-Z0-9_.-] required", *username)
	}
	pass, err := passwordValue(*password)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u, err := db.CreateUser(*username, string(hash), orgs)
	if err != nil {
		return err
	}
	printUser(u)
	return nil
}

func userList(db *globaldb.DB) error {
	users, err := db.ListUsers()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUSERNAME\tORGS\tCREATED")
	for _, u := range users {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", u.ID, u.Username, strings.Join(u.Orgs, ","), u.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	return w.Flush()
}

func userShow(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user show", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	fs.Parse(args)
	u, err := lookupByUsername(db, *username)
	if err != nil {
		return err
	}
	printUser(u)
	return nil
}

func userDelete(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user delete", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	fs.Parse(args)
	u, err := lookupByUsername(db, *username)
	if err != nil {
		return err
	}
	if err := db.DeleteUser(u.ID); err != nil {
		return err
	}
	fmt.Printf("deleted user %q (id %d)\n", u.Username, u.ID)
	return nil
}

func userSetPassword(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user set-password", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	password := fs.String("password", "", "new password (prompted if omitted)")
	fs.Parse(args)
	u, err := lookupByUsername(db, *username)
	if err != nil {
		return err
	}
	pass, err := passwordValue(*password)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := db.SetPassword(u.ID, string(hash)); err != nil {
		return err
	}
	fmt.Printf("password updated for %q\n", u.Username)
	return nil
}

func userSetOrgs(db *globaldb.DB, args []string) error {
	fs := flag.NewFlagSet("user set-orgs", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	var orgs stringSlice
	fs.Var(&orgs, "org", "org membership (routing cache), repeatable; replaces the current list")
	fs.Parse(args)
	u, err := lookupByUsername(db, *username)
	if err != nil {
		return err
	}
	if err := db.SetOrgs(u.ID, orgs); err != nil {
		return err
	}
	fmt.Printf("orgs updated for %q: %s\n", u.Username, strings.Join(orgs, ","))
	return nil
}

func lookupByUsername(db *globaldb.DB, username string) (*globaldb.User, error) {
	if username == "" {
		return nil, errors.New("-username is required")
	}
	u, err := db.GetUserByUsername(username)
	if errors.Is(err, globaldb.ErrNotFound) {
		return nil, fmt.Errorf("user %q not found", username)
	}
	return u, err
}

func passwordValue(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if len(raw) == 0 {
		return "", errors.New("password must not be empty")
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	confirm, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if string(raw) != string(confirm) {
		return "", errors.New("passwords do not match")
	}
	return string(raw), nil
}

func printUser(u *globaldb.User) {
	fmt.Printf("id:       %d\nusername: %s\norgs:     %s\ncreated:  %s\n",
		u.ID, u.Username, strings.Join(u.Orgs, ","), u.UpdatedAt.Format("2006-01-02 15:04:05"))
}
