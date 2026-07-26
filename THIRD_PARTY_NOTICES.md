# 第三方软件说明

本项目使用以下主要开源依赖：

- `github.com/shirou/gopsutil/v4` `v4.26.6`
  - 许可证：BSD 3-Clause
- `github.com/spf13/cobra` `v1.10.2`
  - 许可证：Apache License 2.0
- `golang.org/x/sys` `v0.47.0`
  - 许可证：BSD 3-Clause

运行时还会间接使用 `github.com/spf13/pflag` 等模块。完整、可复现的模块图及校验值记录在 `go.mod` 和 `go.sum` 中。

每个发布压缩包都会包含构建时自动生成的 `THIRD_PARTY_LICENSES.txt`。它包含构建该二进制所用 Go 运行时和标准库的 `LICENSE`、`PATENTS`，以及该目标平台实际链接到的第三方 Go 模块随附的完整许可证、版权和免责声明；任何必需授权文件缺失时，发布构建会直接失败。
