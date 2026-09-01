// life-migrate は entries などのスキーマを DB に適用するための単体コマンド。
//
// api（cmd/life）と別プロセスに分けてあるのは、ECS で「マイグレーション用のタスクを
// 1 回流し、成功してからサービスを新しいリビジョンに更新する」という順序を取れる
// ようにするため。api 起動時に自動適用すると、タスクが同時に複数立ち上がったときに
// 同じマイグレーションを取り合うことになる。
//
//	life-migrate up          # 未適用のものを全て適用する
//	life-migrate down        # 直前の 1 つを巻き戻す
//	life-migrate down -n 3   # 3 つ巻き戻す
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

	cmd := flag.Arg(0)
	if cmd == "" {
		usage()
		os.Exit(2)
	}

	// flag.Parse は最初の非フラグ引数で解析を止めるため、この時点では
	// `life-migrate down -n 3` の -n がまだ読まれていない。サブコマンドの
	// 後ろに残った分をもう一度解析して拾う（ExitOnError なので、書式が
	// 不正ならここで usage を出して終わる）。
	rest := flag.Args()[1:]
	_ = flag.CommandLine.Parse(rest)

	// それでも余る引数は打ち間違い。黙って既定値で実行すると、-n が効いて
	// いないことに気づけないままロールバックが 1 つだけ走る。
	if extra := flag.Args(); len(extra) > 0 {
		fmt.Fprintf(os.Stderr, "unknown argument: %q\n\n", extra[0])
		usage()
		os.Exit(2)
	}

	if *dbURLF == "" {
		fatalf("接続先が空です。-db-url か環境変数 DATABASE_URL を指定してください")
	}

	switch cmd {
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
  down      直前のマイグレーションを巻き戻す（-n で数を指定。例: down -n 3）
  version   適用済みのバージョンを表示する

flags:
`)
	flag.PrintDefaults()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "life-migrate: "+format+"\n", args...)
	os.Exit(1)
}
