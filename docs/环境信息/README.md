# 本机开发环境

此目录记录实际安装环境，与业务设计、数据库适配认证和生产容量规划分开维护。数据库安装完成不代表项目已经实现对相应版本的适配。

- [基础软件](基础软件.md)：Windows、WSL2、Docker Desktop、Kubernetes、D 盘目录及资源设置。
- [数据库](数据库.md)：MySQL、Redis、InfluxDB 的安装版本、访问方式、持久化、维护命令和实际验证结果。
- [安装记录](安装记录.json)：本次实测版本、镜像摘要、持久卷路径和 Pod 重建验证记录，不含密码或 Token。

凭据位于本机 `D:\AgentForgeData\credentials`，受 Windows 目录权限保护。仓库不保存密码、Token 或含凭据的 Secret 清单。
