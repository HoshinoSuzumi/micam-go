# 管理界面

飞牛 fpk 的应用卡片打开的就是这个单页应用：配置、启停、连接状态、运行日志。

## 开发

```sh
npm install
npm run dev        # 只是界面，接口会 404
```

接口是 CGI，本地起一个假后端比较麻烦；最省事的办法是 `make fpk` 装到 NAS 上看，
改一次界面也就几十秒。接口契约见 `src/lib/api.ts`。

## 构建

```sh
npm run build      # -> dist/，由 make web 调用
npm run check      # svelte-check：类型与可访问性
```

产物是 `index.html` + 一个 JS 和一个 CSS（约 34 KB gzip），打包时
`packaging/build-fpk.sh` 会把 `dist/` 复制进 `app/www/`。

## 约定

- **所有请求都用相对路径**：页面挂在 `/cgi/ThirdParty/micam/index.cgi/` 下，绝对路径
  会跑到 NAS 根目录去。
- **不回显 passToken**：接口只回 `has_token`，输入框留空表示"保持原值"。
- **轮询而不是长连接**：状态 2 秒一次、日志 4 秒一次，CGI 每次请求一个进程，够用且简单。
