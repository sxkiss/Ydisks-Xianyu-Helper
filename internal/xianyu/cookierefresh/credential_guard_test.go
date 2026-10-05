package cookierefresh

import "testing"

func TestHasLoginIdentity(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"完整登录态", "unb=123456; cookie2=abc; _m_h5_tk=a_1; skt=xyz", true},
		{"只有cna和tfstk", "cna=abc; tfstk=def", false},
		{"只有unb缺cookie2", "unb=123456; cna=abc", false},
		{"空串", "", false},
		{"只有cookie2", "cookie2=abc", false},
	}
	for _, c := range cases {
		if got := HasLoginIdentity(c.in); got != c.want {
			t.Errorf("%s: HasLoginIdentity=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestGuardCredentialReplacement_MissingUnb 复现 2026-10-05 线上事故：
// 风控恢复把完整 Cookie 覆盖成只剩 cna/tfstk 的临时罐，导致账号永久掉线。
func TestGuardCredentialReplacement_MissingUnb(t *testing.T) {
	original := "unb=331540304; cookie2=xyz; _m_h5_tk=t_1; skt=abc; x5sec=old"
	degraded := "cna=abc; tfstk=def"

	err := GuardCredentialReplacement(original, degraded)
	if err == nil {
		t.Fatal("残缺 Cookie 覆盖完整登录态时必须报错，实际放行")
	}
	if !strings_Contains(err.Error(), "unb") {
		t.Fatalf("错误信息应指出缺失 unb，实际: %v", err)
	}
}

func TestGuardCredentialReplacement_AllowsSafeWrites(t *testing.T) {
	original := "unb=331540304; cookie2=xyz; _m_h5_tk=t_1; skt=abc"
	cases := []struct {
		name      string
		original  string
		in        string
	}{
		{"带新x5sec的正常增量", original, "unb=331540304; cookie2=xyz; _m_h5_tk=t_1; skt=abc; x5sec=new"},
		{"原凭证本就无登录态", "cna=abc", "cna=abc; tfstk=def"},
		{"刷新了token值", original, "unb=331540304; cookie2=xyz; _m_h5_tk=t_2; skt=abc"},
	}
	for _, c := range cases {
		if err := GuardCredentialReplacement(c.original, c.in); err != nil {
			t.Errorf("%s: 不应拦截，实际: %v", c.name, err)
		}
	}
}

func TestMissingLoginCookieNames(t *testing.T) {
	missing := MissingLoginCookieNames("cna=abc; tfstk=def")
	want := map[string]bool{"unb": true, "cookie2": true, "_m_h5_tk": true, "skt": true}
	if len(missing) != len(want) {
		t.Fatalf("缺失字段数=%d, want %d (%v)", len(missing), len(want), missing)
	}
	for _, name := range missing {
		if !want[name] {
			t.Errorf("出现意外缺失字段: %s", name)
		}
	}
}

// strings_Contains 避免为一个断言引入额外依赖。
func strings_Contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
