# gofast

`gofast` は `go build` の前段でバイナリキャッシュを利用し、同じ入力条件であれば再ビルドを高速化するための **third-party tool** です。  
Go 本体コマンドへの統合を前提にせず、単独コマンドとして利用する想定です。

## セットアップ（導入手順）

このリポジトリをローカルに clone した前提で、次の手順で `gofast` バイナリを作成できます。

```bash
cd /path/to/go/src
./make.bash
../bin/go build -o /path/to/bin/gofast cmd/gofast
```

`/path/to/bin` を `PATH` に追加すると `gofast` を直接実行できます。

## 使い方

```bash
gofast build [--explain-cache] [go build args...]
gofast watch [--explain-cache] [--debounce duration] [go build args...]
gofast clear
gofast verify-layout
```

例:

```bash
gofast build --explain-cache -o app .
gofast watch --debounce=500ms -o app .
gofast clear
```

## 補足

- キャッシュはユーザーキャッシュ配下（`<user cache dir>/gofast`）に保存されます。
- `gofast build` は安全な再利用条件を満たさない場合、自動的に通常の `go build` にフォールバックします。
