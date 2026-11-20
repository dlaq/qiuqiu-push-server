# 毛线球球品牌资源

`yarn-ball-foreground.png` 是通过内置 image_gen 生成的透明栅格前景，亦用于 APP 内品牌图及后台插画；没有使用外部平台 Logo。Android 自适应图标组合此图与 `#EAF5F1` 背景。`adaptive-background.svg` 是可编辑背景源，`notification-small-icon.xml` 为 Android 单色矢量小图标。业务单色图标和启动画面见 `android/app/src/main/res/drawable/`。

Android 使用 108dp 自适应图层。完整桌面图标由系统进行圆形、方圆形等掩模合成，最低 Android 8.0；无需依赖旧式固定圆角位图。启动画面提供 Android 12 系统 SplashScreen 和旧版本窗口背景资源。

生成模式：内置 imagegen，透明背景。项目已保存最终图，不依赖生成服务缓存路径。

最终提示词：

> Use case: logo-brand. Asset type: QiuQiu Push Android adaptive icon foreground and in-app brand illustration. A single adorable cartoon ball of yarn with clear softly wrapped yarn strands, a tiny friendly face, soft bright pastel colors, simple highly recognizable silhouette. Centered composition, yarn ball including a short curved loose yarn end fits entirely inside the central 62% of a square canvas so Android adaptive masks never clip it. Gently dimensional polished illustration with soft texture, suitable at small icon sizes. Transparent background, no text, no letters, no platform logos, no border, no phone mockup. Deliver one isolated yarn ball character only.
