# Switchyard（调车场）

Switchyard 是给 coding agent 用的轻量模型中转站。

它是 Railways 的 Go 重写版，部署在 RackNerd 上、专门只做中转站（该机不再兼梯子；AWS 的梯子不动）。这个仓的前端是从 Magpie UI 改过来的，用户喜欢它的布局和交互。原项目是
[wxm0394/magpie](https://github.com/wxm0394/magpie)（yetone/magpie 的 fork，MIT 协议）。

## 当前状态：只是前端展示，还不是能用的网关

- 前端：React 19 + Vite + Tailwind，`src/App.tsx`、`src/types.ts`。
  跑法：`npm install`，然后 `npm run dev`（展示服务器，3000 端口），`npm run build` 打静态包，`npm run lint` 跑 `tsc`。
- 展示后端：`server.ts` 只是 Express + Vite 的展示服务器，给的是内存里的种子数据（从 Magpie UI 展示改过来的）。它不是 Railways 网关：没有 Go 代码，没有别名、没有轮转、没有熔断、没有 Kaggle 定时器、没有真实上游路由、没有加密 Key。
- Go 后端：用户在做，按 Railways 的 API 契约来（客户端 `POST /v1/chat/completions` + `GET /v1/models`，管理端 `/api/gateway/*`）。视图一级的对应关系见 `RAILWAYS_ADAPTATION.md`。
- 改名：`package.json`、`index.html`、`metadata.json` 和界面上看得见的 Magpie 字样已换成 Switchyard。代码里剩下的 Magpie 字样是出处说明（“adapted from Magpie UI”）和种子数据（例如 `@magpie-community/*` 插件包名），等数据模型适配时再换。

## 还留在仓里的 Magpie 原件，待替换

- `docs/`：Magpie 子系统文档，只作参考，不描述 Switchyard。
- `Dockerfile`：Magpie 的 Go Dockerfile，现在编不出 Switchyard（没有 `go.mod` / `main.go`），Go 后端落地时必须换掉。
- `AGENTS.md`、`CLAUDE.md`、`LICENSE`、`THIRD_PARTY_NOTICES`：Magpie 原件，作参考 / 协议用。仓里 Magpie 的代码仍按 MIT 协议。

## 路线图

1. 改名干净（本 PR）：用户看得见的文件换成 Switchyard。
2. 按 `RAILWAYS_ADAPTATION.md` 把界面数据模型改成 Railways。
3. Go 后端按 Railways API 契约接前端，替换内存展示服务器。
4. 迁到 RackNerd 专门做中转站；AWS 梯子不动。
