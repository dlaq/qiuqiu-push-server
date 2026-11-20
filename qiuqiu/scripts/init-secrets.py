#!/usr/bin/env python3
"""Initialize local deployment secrets without printing passwords."""
import getpass, pathlib, secrets, subprocess, os
root=pathlib.Path(__file__).resolve().parents[1]
directory=root/'deploy/secrets'
directory.mkdir(mode=0o700,exist_ok=True)
admin=directory/'admin_password'
if not admin.exists():
    password=getpass.getpass('首次管理员密码（至少12字节）：')
    if len(password.encode())<12 or len(password.encode())>72:raise SystemExit('密码长度必须为12–72字节')
    if password!=getpass.getpass('再次输入：'):raise SystemExit('密码不一致')
    admin.write_text(password);admin.chmod(0o600)
p=directory/'ntfy_admin_password'
if not p.exists():p.write_text(secrets.token_hex(32));p.chmod(0o600)
h=directory/'ntfy_admin_hash'
if not h.exists():
    # bcrypt is supplied by Ubuntu python3-bcrypt or a task-local venv.
    try:import bcrypt
    except ImportError:
        binary=pathlib.Path(os.environ.get('QQ_GATEWAY_BIN',str(root/'releases/qiuqiu-gateway-linux-amd64')))
        if binary.exists():command=[str(binary),'hash-password']
        else:command=['docker','run','--rm','-i',os.environ.get('QQ_GATEWAY_IMAGE','qiuqiu-push-gateway:latest'),'hash-password']
        try:value=subprocess.run(command,input=p.read_bytes(),capture_output=True,check=True).stdout.decode()
        except (OSError,subprocess.CalledProcessError):raise SystemExit('请先运行 docker compose build，或安装 python3-bcrypt，然后重试')
    else:value=bcrypt.hashpw(p.read_bytes(),bcrypt.gensalt()).decode()
    h.write_text(value);h.chmod(0o600)
print('凭证已就绪，未输出密码。请编辑 deploy/.env 配置两个域名。')
