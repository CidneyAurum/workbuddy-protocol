# 来源与归属

此仓库是个人私有源码备份与协议整理，源于本机当前的 WorkBuddy Go 网关及本地兼容模块。未导入原仓库的 Git 历史，也不将这份包含本地修改的快照称为某个上游提交的原封副本。

## 原项目

本机源码附带的 README 明确标注原始出处为 [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api)。其说明、归属、许可及其他原始声明保存在 [docs/UPSTREAM_README.md](docs/UPSTREAM_README.md)；仅将与本地配置值重合的 Key 示例改为不同的占位符，未修改许可声明。

本机保存的公开分支 [HanawaBanana/workbuddy2api](https://github.com/HanawaBanana/workbuddy2api)，提交 `af5e77947c93fd6b7f8b3815ee047d64e0b97752`，带有 Sliverkiss 的 MIT 原文；本次逐字复制到 [LICENSES/Sliverkiss-workbuddy2api-MIT.txt](LICENSES/Sliverkiss-workbuddy2api-MIT.txt)。

本机也保存了 [misakano7545/workbuddy2api-panel](https://github.com/misakano7545/workbuddy2api-panel) 的许可文件，其中保留 Sliverkiss、linguo2625469 和 misakano7545 的版权与来源说明。完整原文保存在 [LICENSES/WorkBuddy-panel-MIT.txt](LICENSES/WorkBuddy-panel-MIT.txt)，不据此认定每个本地文件都来自该分支。

## 本地维护部分

当前目录没有完整来源历史，部分源码已被本地修改，另含没有独立许可文件的本地 `anthropiccompat` 模块。本次没有为这些修改虚构作者、上游版本或统一许可证。Go 第三方依赖由 `go.mod` / `go.sum` 记录，未将依赖源码打包入库。

原有 README 同时含有 MIT 声明、用途与再分发限制以及“免责声明优先”的文字；原源码目录没有独立 LICENSE。本仓库如实保存这些材料，不通过整理动作修改、扩大或重新授予任何原有权利。

本次新增协议文档、导航、示例配置和快照清单用于说明这份私有备份；导出调整与原始源文件摘要见 [SOURCE_MANIFEST.json](SOURCE_MANIFEST.json)。
