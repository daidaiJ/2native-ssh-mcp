package approval

import "testing"

func TestBuiltinDestructive(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"rm -rf /tmp/build", true},
		{"sudo rm -rf /", true},
		{"cd /tmp && rm -rf cache", true},
		{"ls $(rm -rf /)", true},
		{"`reboot`", true},
		{"find . | xargs rm", true},
		{"shutdown -h now", true},
		{"reboot", true},
		{"init 6", true},
		{"init 3", false},
		{"dd if=a.iso of=/dev/sdb", true},
		{"dd if=/dev/zero of=disk.img bs=1M count=100", false},
		{"mkfs.ext4 /dev/sda1", true},
		{"echo hello", false},
		{"echo $HOME", false},
		{"ls -la /var/log", false},
		{"systemctl status nginx", false},
		{"systemctl restart nginx", true},
		{"systemctl stop nginx", true},
		{"service nginx restart", true},
		{"chmod +x deploy.sh", false},
		{"chmod -R 777 /var/www", true},
		{"chown -R www:www /var/www", true},
		{"chmod 644 index.html", false},
		{"crontab -l", false},
		{"crontab -r", true},
		{"echo foo > /etc/passwd", true},
		{"echo key > ~/.ssh/authorized_keys", true},
		{"echo foo > /tmp/out.txt", false},
		{"iptables -F", true},
		{"iptables -L", false},
		{":(){ :|:& };:", true},
		{"git push --force origin main", true},
		{"git push origin main", false},
		{"git reset --hard HEAD~1", true},
		{"kill -9 1234", true},
		{"killall nginx", true},
		{"pkill -f worker", true},
		{"mysql -e 'DROP DATABASE prod'", true},
		{"Remove-Item -Recurse -Force C:\\tmp", true},
		{"del /s /q C:\\tmp", true},
		{"timeout 30 reboot", true},
		{"", false},
		{"cat /etc/os-release | grep PRETTY", false},
	}
	for _, c := range cases {
		got, reason := IsDestructive(c.cmd, nil, nil)
		if got != c.want {
			t.Errorf("IsDestructive(%q) = %v (reason %q), want %v", c.cmd, got, reason, c.want)
		}
	}
}

func TestUserPatternsAndExemptions(t *testing.T) {
	extra := []string{`terraform\s+destroy\b`}
	exempt := []string{`^kill\s+-9\s+\d+$`, `rm\s+-rf\s+/tmp/`}

	// Extra patterns widen the set.
	if ok, _ := IsDestructive("terraform destroy -auto-approve", extra, nil); !ok {
		t.Error("expected approvalPatterns to widen the destructive set")
	}
	if ok, _ := IsDestructive("terraform destroy -auto-approve", nil, nil); ok {
		t.Error("terraform destroy should not be destructive without extra patterns")
	}
	// Exempt patterns win over built-ins.
	if ok, _ := IsDestructive("kill -9 4242", nil, exempt); ok {
		t.Error("expected exempt pattern to suppress kill -9")
	}
	if ok, _ := IsDestructive("rm -rf /tmp/cache", nil, exempt); ok {
		t.Error("expected exempt pattern to suppress rm -rf /tmp/")
	}
	// Exempt does not leak to other commands.
	if ok, _ := IsDestructive("rm -rf /etc", nil, exempt); !ok {
		t.Error("rm -rf /etc must stay destructive")
	}
}

func TestInvalidUserRegexIsNotDestructive(t *testing.T) {
	// A bad regex must not panic or match; config validation reports it at
	// load time, classification just ignores it.
	if ok, _ := IsDestructive("rm -rf /", []string{"(["}, nil); !ok {
		t.Error("builtin rule should still fire")
	}
	if ok, _ := IsDestructive("ls", nil, []string{"(["}); ok {
		t.Error("invalid exempt regex must not match anything")
	}
}
