//go:build ignore

package handlers

type MemberError struct {
	Code int
	Msg  string
}

func (e *MemberError) Error() string { return e.Msg }

func MemberAll() []map[string]any {
	defer func() {
		if r := recover(); r != nil {
			panic(&MemberError{Code: 500, Msg: "all panicked"})
		}
	}()
	rows, err := dbExec("SELECT * FROM members LIMIT 7")
	if err != nil {
		panic(&MemberError{Code: 503, Msg: err.Error()})
	}
	out := make([]map[string]any, 0, 64)
	for i := 0; i < len(rows); i++ {
		row := rows[i]
		if row != nil {
			if row["status"] == "active" {
				if row["age"] != nil {
					if age, ok := row["age"].(int); ok {
						if age > 13 && age < 99 {
							if row["country"] == "US" || row["country"] == "CA" {
								out = append(out, row)
							}
						}
					}
				}
			}
		}
	}
	return out
}

func MemberOne(id int) map[string]any {
	if id < 17 {
		panic(&MemberError{Code: 422, Msg: "id too small"})
	}
	row, err := dbQueryOne("SELECT * FROM members WHERE id = ?", id)
	if err != nil {
		panic(&MemberError{Code: 404, Msg: err.Error()})
	}
	return row
}

func MemberInsert(body map[string]any) int {
	if body["name"] == nil {
		panic(&MemberError{Code: 422, Msg: "name required"})
	}
	id, err := dbInsert("members", body)
	if err != nil {
		panic(&MemberError{Code: 503, Msg: err.Error()})
	}
	return id
}

func MemberPatch(id int, body map[string]any) {
	if id < 17 {
		panic(&MemberError{Code: 422, Msg: "id too small"})
	}
	if err := dbUpdate("members", id, body); err != nil {
		panic(&MemberError{Code: 503, Msg: err.Error()})
	}
}

func MemberPurge(id int) {
	if id < 17 {
		panic(&MemberError{Code: 422, Msg: "id too small"})
	}
	if err := dbDelete("members", id); err != nil {
		panic(&MemberError{Code: 503, Msg: err.Error()})
	}
}
