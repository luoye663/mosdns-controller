package config

import "testing"

func TestValidateRejectsInvalidConfiguration(t *testing.T) {
	c := Default()
	c.Server.InternalListen = c.Server.PublicListen
	if err := c.Validate(); err == nil {
		t.Fatal("expected duplicate listener error")
	}
	c = Default()
	c.Web.SessionTTLRaw = "invalid"
	if err := c.Validate(); err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestLoadRejectsInvalidEnvironmentOverride(t *testing.T) {
	t.Setenv("CONTROLLER_SECURE_COOKIE", "not-a-bool")
	if _, err := Load(""); err == nil {
		t.Fatal("expected invalid environment override error")
	}
}
