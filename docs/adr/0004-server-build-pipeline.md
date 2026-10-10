# 服务端 esbuild 构建 + vendor React

生成的 React + TypeScript 工程由服务端 esbuild（官方 Go API，嵌在 server 二进制内）编译为静态产物（dist/main.js + main.css + 重写后的 index.html），iframe 预览直接伺服该产物。react、react-dom、scheduler 的 ESM 构建预置于 internal/builder/vendor/ 并经 go:embed 打入二进制，esbuild 插件将 react / react/jsx-runtime / react-dom/client 等解析至该目录——构建和预览零网络依赖，任何环境下行为确定（Q9/Q11 决策）。

拒绝过的替代：每会话 npm install + vite build（真实但每次数十秒且依赖 registry 网络，现场翻车概率高）；浏览器内 esbuild-wasm（内存开销大、首次加载慢）。React 版本被 vendor 目录钉死，升级即替换文件；生成工程被约束为仅可 import react / react-dom（Q10），与 vendor 内容互为表里。
