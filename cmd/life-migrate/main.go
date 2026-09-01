// life-migrate は entries などのスキーマを DB に適用するための単体コマンド。
//
// api（cmd/life）と別プロセスに分けてあるのは、ECS で「マイグレーション用のタスクを
// 1 回流し、成功してからサービスを新しいリビジョンに更新する」という順序を取れる
// ようにするため。api 起動時に自動適用すると、タスクが同時に複数立ち上がったときに
// 同じマイグレーションを取り合うことになる。
//
//	life-migrate up          # 未適用のものを全て適用する
//	life-migrate down        # 直前の 1 つを巻き戻す（-n で数を指定）
//	life-migrate version     # 適用済みのバージョンを表示する
//
// 接続先は -db-url、既定は環境変数 DATABASE_URL。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/e601201/life-api/db"
)

func main() {
	var (
		dbURLF = flag.String("db-url", os.Getenv("DATABASE_URL"), "PostgreSQL connection URL (default $DATABASE_URL)")
		stepsF = flag.Int("n", 1, "down で巻き戻すマイグレーションの数")
	)
	flag.Usage = usage
	flag.Parse()

	if *dbURLF == "" {
		fatalf("接続先が空です。-db-url か環境変数 DATABASE_URL を指定してください")
	}

	switch cmd := flag.Arg(0); cmd {
	case "up":
		if err := db.Up(*dbURLF); err != nil {
			fatalf("%v", err)
		}
		printVersion(*dbURLF, "migrated")

	case "down":
		if err := db.Down(*dbURLF, *stepsF); err != nil {
			fatalf("%v", err)
		}
		printVersion(*dbURLF, "rolled back")

	case "version":
		printVersion(*dbURLF, "current")

	case "":
		usage()
		os.Exit(2)

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

// printVersion は現在の適用状況を1行で出す。up / down の後にも呼んで、
// 何版まで進んだ（戻った）かをログに残す。
func printVersion(dbURL, label string) {
	v, dirty, applied, err := db.Version(dbURL)
	if err != nil {
		fatalf("%v", err)
	}
	if !applied {
		fmt.Printf("%s: no migrations applied\n", label)
		return
	}
	if dirty {
		// dirty のまま放置すると以降の up / down が全て弾かれるので、目立たせる。
		fmt.Printf("%s: version %d (DIRTY: 適用の途中で失敗しています)\n", label, v)
		return
	}
	fmt.Printf("%s: version %d\n", label, v)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: life-migrate [flags] <up|down|version>

  up        未適用のマイグレーションを全て適用する
  down      直前のマイグレーションを巻き戻す（-n で数を指定）
  version   適用済みのバージョンを表示する

flags:
`)
	flag.PrintDefaults()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "life-migrate: "+format+"\n", args...)
	os.Exit(1)
}
