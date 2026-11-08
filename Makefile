.PHONY: run build stage stop status logs migrate backup
export ARTIFACT

# Linux 单机环境：使用 /etc/agentforge-ontology 配置及现有持久化数据库。
# run 只安装本地/CI 生成的制品，停止旧进程并启动新进程后返回。
run stage stop status logs migrate backup:
	bash scripts/server-dev.sh $@

# 仅在开发机执行；业务服务器拒绝编译。
build:
	python3 scripts/build-release.py
