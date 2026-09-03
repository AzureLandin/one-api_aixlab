# One API 的前端界面

本系统只保留 **default** 一个前端主题（内网部署不再提供多主题切换），
构建产物固定输出到 `web/build/default`，并由 `main.go` 的 `//go:embed web/build/*` 打包进二进制。

## 目录结构

- `default/`：唯一主题，React + Semantic UI。
- `build/`：各主题的构建产物目录（内容不入库，构建时生成）。
- `THEMES`：参与构建的主题列表，本仓库只有 `default`。
- `build.sh`：按 `THEMES` 逐个执行 `npm install && npm run build`。

## 主题：default

默认主题，继承自上游 [One API](https://github.com/songquanpeng/one-api)。

预览：
|![image](https://github.com/songquanpeng/one-api/assets/39998050/ccfbc668-3a7f-4bc1-87da-7eacfd7bf371)|![image](https://github.com/songquanpeng/one-api/assets/39998050/a63ed547-44b9-45db-b43a-ecea07d60840)|
|:---:|:---:|

## 本地开发与构建

```bash
cd web/default
npm install
npm start          # 本地开发服务器
npm run build      # 构建并把产物移动到 ../build/default
```

> `npm run build` 脚本使用 `rm -rf` / `mv`，需在类 Unix shell（Linux、macOS、WSL、Git Bash）下执行。

## 若确实需要新增主题

1. 在 `web` 文件夹下新建以主题名命名的目录。
2. 修改其 `package.json` 的 `build` 命令，把产物移动到 `../build/<主题名>`。
3. 在 `common/config/config.go` 的 `ValidThemes` 中注册该主题名。
4. 同步修改 `web/THEMES` 与 `Dockerfile` 的构建步骤。
5. 通过环境变量 `THEME=<主题名>` 指定运行时主题（无法在设置页修改）。
