// Rafei Ahamed - 10696700
// templates.go - the web GUI: HTML templates and CSS kept as Go strings so the
// finished program is a single self-contained binary with no asset folder.
//
// html/template escapes every value for the context it appears in (HTML text,
// attribute, URL), which protects the pages against cross-site scripting.
package main

const css = `
:root{--bg:#f4f6fb;--card:#fff;--ink:#1c2333;--mute:#5d6678;--brand:#1f4fd8;--line:#dfe3ee;--ok:#16794c;--bad:#b3261e}
*{box-sizing:border-box}
body{margin:0;font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;background:var(--bg);color:var(--ink)}
.bar{display:flex;justify-content:space-between;align-items:center;padding:.8rem 1.5rem;background:var(--brand);color:#fff}
.bar a{color:#fff;text-decoration:none}.brand{font-weight:700;font-size:1.1rem}
.bar nav{display:flex;gap:1rem;align-items:center}
main{max-width:980px;margin:1.5rem auto;padding:0 1rem}
h1{margin:.2rem 0 1rem}h2{margin-top:1.8rem}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:1.2rem;margin-bottom:1rem}
table{width:100%;border-collapse:collapse;background:var(--card)}
th,td{padding:.55rem .7rem;border-bottom:1px solid var(--line);text-align:left;vertical-align:middle}
th a{color:inherit;text-decoration:none}th a:hover{text-decoration:underline}
th{background:#eef1f9;font-size:.85rem;text-transform:uppercase;letter-spacing:.03em}
label{display:block;font-weight:600;margin:.8rem 0 .2rem}
input,select{width:100%;padding:.5rem;border:1px solid #b9c0d4;border-radius:6px;font:inherit}
input.mark{width:5rem}input[readonly]{background:#eef1f9}
button,.btn{display:inline-block;padding:.5rem .9rem;border:0;border-radius:6px;background:var(--brand);color:#fff;font:inherit;cursor:pointer;text-decoration:none}
button.danger{background:var(--bad)}button.link{background:none;padding:0;text-decoration:underline}
.inline{display:inline-flex;gap:.4rem;align-items:center;margin:0}
.flash{padding:.7rem 1rem;border-radius:8px;margin-bottom:1rem;border:1px solid}
.flash.ok{background:#e6f5ee;color:var(--ok);border-color:#a7d7c0}
.flash.error,.err{background:#fdecea;color:var(--bad);border-color:#f2b8b5}
.err{display:block;background:none;font-size:.88rem;margin-top:.2rem}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:1rem}
.stat{font-size:1.8rem;font-weight:700}.mute{color:var(--mute)}
.portal{display:block;color:var(--ink);text-decoration:none;text-align:center;padding:1.6rem}.portal:hover{border-color:var(--brand)}.portal strong{font-size:1.2rem;color:var(--brand)}
.toolbar{display:flex;gap:.6rem;margin-bottom:1rem}.toolbar input{flex:1}
details summary{cursor:pointer;color:var(--bad);margin-top:1rem}
`

const allTemplates = `
{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} - Student Record System</title>
<style>` + css + `</style></head><body>
<header class="bar"><a class="brand" href="/">Student Record System</a>
{{if .User}}<nav>{{if .IsAdmin}}<a href="/">Students</a><a href="/units">Units</a>{{else}}<a href="/">My record</a>{{end}}<a href="/account/password">Change password</a><span>{{.User.Username}} <em>({{.User.Role}})</em></span>
<form method="post" action="/logout" class="inline"><input type="hidden" name="_csrf" value="{{.CSRF}}"><button class="link">Sign out</button></form></nav>{{end}}
</header><main>
{{with .Flash}}<div class="flash {{.Kind}}" role="status">{{.Msg}}</div>{{end}}
{{end}}

{{define "foot"}}</main></body></html>{{end}}

{{define "welcome"}}{{template "head" .}}
<div class="card" style="max-width:680px;margin:2rem auto;text-align:center">
<h1>Student Record System</h1>
<p class="mute">Choose how you want to sign in.</p>
<div class="grid">
<a class="card portal" href="/login/admin"><strong>Administrator</strong><br><span class="mute">Manage students, units, enrolments and marks</span></a>
<a class="card portal" href="/login/student"><strong>Student</strong><br><span class="mute">View your record and manage your own enrolments</span></a>
</div></div>
{{template "foot" .}}{{end}}

{{define "login"}}{{template "head" .}}
<div class="card" style="max-width:420px;margin:2rem auto">
<h1>{{.Title}}</h1>
{{with .Error}}<div class="flash error" role="alert">{{.}}</div>{{end}}
<form method="post" action="/login/{{.Portal}}">
<label for="username">{{if eq .Portal "student"}}Student ID{{else}}Username{{end}}</label><input id="username" name="username" autocomplete="username" required autofocus>
<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>
<p><button>Sign in</button> <a href="/">Choose a different sign-in</a></p>
</form></div>
{{template "foot" .}}{{end}}

{{define "students"}}{{template "head" .}}
<h1>Students</h1>
{{with .Stats}}<div class="grid">
<div class="card"><div class="mute">Students shown</div><div class="stat">{{.Total}}</div></div>
<div class="card"><div class="mute">Average WAM</div><div class="stat">{{.AverageWAM}}</div></div>
<div class="card"><div class="mute">WAM below 60</div><div class="stat">{{.NeedSupport}}</div></div>
</div>{{end}}
<form class="toolbar" method="get" action="/">
<input name="q" value="{{.Query}}" placeholder="Search by ID, name or course" aria-label="Search students">
<button>Search</button><a class="btn" href="/students/new">+ Add student</a>
<a class="btn" style="background:#16794c" href="/export.csv?q={{.Query}}&amp;sort={{.Sort}}">Export CSV</a>
</form>
<table><thead><tr>
<th><a href="/?q={{.Query}}&amp;sort=id">ID</a></th><th><a href="/?q={{.Query}}&amp;sort=name">Name</a></th><th><a href="/?q={{.Query}}&amp;sort=course">Course</a></th><th><a href="/?q={{.Query}}&amp;sort=wam">WAM</a></th><th>Credit points</th></tr></thead><tbody>
{{range .Rows}}<tr><td><a href="/students/{{.ID}}">{{.ID}}</a></td><td>{{.Name}}</td><td>{{.Course}}</td><td>{{.WAM}}</td><td>{{.CreditPoints}}</td></tr>
{{else}}<tr><td colspan="5" class="mute">No students match.</td></tr>{{end}}
</tbody></table>
{{template "foot" .}}{{end}}

{{define "password"}}{{template "head" .}}
<div class="card" style="max-width:460px;margin:2rem auto">
<h1>Change password</h1>
{{with .Error}}<div class="flash error" role="alert">{{.}}</div>{{end}}
<form method="post" action="/account/password"><input type="hidden" name="_csrf" value="{{.CSRF}}">
<label for="current">Current password</label><input id="current" name="current" type="password" autocomplete="current-password" required>
<label for="new">New password (8 to 128 characters)</label><input id="new" name="new" type="password" autocomplete="new-password" minlength="8" maxlength="128" required>
<label for="confirm">Confirm new password</label><input id="confirm" name="confirm" type="password" autocomplete="new-password" required>
<p><button>Change password</button> <a class="btn" style="background:#6b7490" href="/">Cancel</a></p>
</form></div>
{{template "foot" .}}{{end}}

{{define "student"}}{{template "head" .}}
{{with .View}}
<h1>{{.Student.FullName}} <span class="mute">#{{.Student.ID}}</span></h1>
<div class="grid">
<div class="card"><div class="mute">WAM</div><div class="stat">{{.WAMText}}</div><div class="mute">{{.CompletedCP}} credit points completed</div></div>
<div class="card"><div class="mute">Course</div><div><strong>{{.Student.CourseCode}}</strong></div><div class="mute">{{.CourseName}}</div></div>
<div class="card"><div class="mute">Contact</div><div>{{.Student.Email}}</div><div class="mute">{{if .Student.Phone}}{{.Student.Phone}}{{else}}No phone on file{{end}} &middot; born {{.Student.DateOfBirth}}</div></div>
</div>
{{if .Admin}}<p><a class="btn" href="/students/{{.Student.ID}}/edit">Edit details</a></p>{{end}}

<h2>Unit enrolments</h2>
<table><thead><tr><th>Unit</th><th>Title</th><th>Period</th><th>CP</th><th>Mark</th><th>Grade</th><th>Actions</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{.UnitCode}}</td><td>{{.Title}}</td><td>{{.Year}} S{{.Semester}}</td><td>{{.CP}}</td><td>{{.MarkText}}</td><td>{{.Grade}}</td>
<td>
{{if $.View.Admin}}<form method="post" action="/students/{{$.View.Student.ID}}/mark" class="inline">
<input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="unit" value="{{.UnitCode}}"><input type="hidden" name="year" value="{{.Year}}"><input type="hidden" name="semester" value="{{.Semester}}">
<input class="mark" name="mark" type="number" min="0" max="100" value="{{.MarkValue}}" aria-label="Mark for {{.UnitCode}}"><button>Save</button></form>{{end}}
{{if or $.View.Admin (not .MarkValue)}}<form method="post" action="/students/{{$.View.Student.ID}}/unenrol" class="inline">
<input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="unit" value="{{.UnitCode}}"><input type="hidden" name="year" value="{{.Year}}"><input type="hidden" name="semester" value="{{.Semester}}"><button class="danger">Remove</button></form>{{end}}
</td></tr>
{{else}}<tr><td colspan="7" class="mute">Not enrolled in any units yet.</td></tr>{{end}}
</tbody></table>

<div class="card" style="margin-top:1rem"><h2 style="margin-top:0">Enrol in a unit</h2>
<form method="post" action="/students/{{.Student.ID}}/enrol"><input type="hidden" name="_csrf" value="{{$.CSRF}}">
<div class="grid">
<div><label for="unit">Unit</label><select id="unit" name="unit">{{range $.Units}}<option value="{{.Code}}">{{.Code}} - {{.Title}} ({{.CreditPoints}} CP)</option>{{end}}</select></div>
<div><label for="year">Year</label><input id="year" name="year" type="number" value="{{$.Year}}" min="{{.MinYear}}" max="{{.MaxYear}}" required></div>
<div><label for="semester">Semester</label><select id="semester" name="semester"><option>1</option><option>2</option></select></div>
</div><p><button>Enrol</button></p>
{{if not .Admin}}<p class="mute">You can enrol for {{.MinYear}} or {{.MaxYear}}, and remove a unit until it has a result.</p>{{end}}
</form></div>
{{if .Admin}}
<details><summary>Delete this student</summary>
<form method="post" action="/students/{{.Student.ID}}/delete"><input type="hidden" name="_csrf" value="{{$.CSRF}}">
<p>This permanently removes the record and the login account.</p><button class="danger">Yes, delete {{.Student.FullName}}</button></form></details>
{{end}}
{{end}}
{{template "foot" .}}{{end}}

{{define "units"}}{{template "head" .}}
<h1>Units</h1>
<div class="card"><h2 style="margin-top:0">Create a unit</h2>
<form method="post" action="/units"><input type="hidden" name="_csrf" value="{{.CSRF}}">
<div class="grid">
<div><label for="code">Unit code (e.g. CSP4401)</label><input id="code" name="code" maxlength="7" required></div>
<div><label for="title">Title</label><input id="title" name="title" maxlength="80" required></div>
<div><label for="newcp">Credit points</label><select id="newcp" name="creditPoints">{{range .CPOptions}}<option {{if eq . 15}}selected{{end}}>{{.}}</option>{{end}}</select></div>
</div><p><button>Create unit</button></p></form></div>
<table><thead><tr><th>Code</th><th>Title and credit points (edit, then Save)</th><th>Enrolments</th><th>Remove</th></tr></thead><tbody>
{{range .UnitRows}}{{$row := .}}<tr><td>{{.Code}}</td>
<td><form method="post" action="/units/{{.Code}}" class="inline"><input type="hidden" name="_csrf" value="{{$.CSRF}}">
<input name="title" value="{{.Title}}" maxlength="80" required aria-label="Title for {{.Code}}" style="min-width:18rem">
<select name="creditPoints" aria-label="Credit points for {{.Code}}" style="width:5rem">{{range $.CPOptions}}<option {{if eq . $row.CreditPoints}}selected{{end}}>{{.}}</option>{{end}}</select>
<button>Save</button></form></td>
<td>{{.Enrolments}}</td>
<td>{{if gt .Enrolments 0}}<span class="mute">In use</span>{{else}}<form method="post" action="/units/{{.Code}}/delete" class="inline"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><button class="danger">Remove</button></form>{{end}}</td></tr>
{{else}}<tr><td colspan="4" class="mute">No units yet.</td></tr>{{end}}
</tbody></table>
<p class="mute">A unit can only be removed when no student is enrolled in it. Changing credit points changes every affected WAM.</p>
{{template "foot" .}}{{end}}

{{define "form"}}{{template "head" .}}
<h1>{{if .Form.IsEdit}}Edit student{{else}}Add student{{end}}</h1>
<div class="card">
<form method="post" action="{{if .Form.IsEdit}}/students/{{.Form.Student.ID}}{{else}}/students{{end}}" novalidate>
<input type="hidden" name="_csrf" value="{{.CSRF}}">
<label for="id">Student ID (8 digits)</label>
<input id="id" name="id" value="{{.Form.Student.ID}}" inputmode="numeric" maxlength="8" {{if .Form.IsEdit}}readonly{{end}}>
{{with index .Form.Errors "id"}}<span class="err">{{.}}</span>{{end}}
<div class="grid"><div>
<label for="firstName">First name</label><input id="firstName" name="firstName" value="{{.Form.Student.FirstName}}">
{{with index .Form.Errors "firstName"}}<span class="err">{{.}}</span>{{end}}</div><div>
<label for="lastName">Last name</label><input id="lastName" name="lastName" value="{{.Form.Student.LastName}}">
{{with index .Form.Errors "lastName"}}<span class="err">{{.}}</span>{{end}}</div></div>
<label for="email">Email</label><input id="email" name="email" type="email" value="{{.Form.Student.Email}}">
{{with index .Form.Errors "email"}}<span class="err">{{.}}</span>{{end}}
<div class="grid"><div>
<label for="dateOfBirth">Date of birth</label><input id="dateOfBirth" name="dateOfBirth" type="date" value="{{.Form.Student.DateOfBirth}}">
{{with index .Form.Errors "dateOfBirth"}}<span class="err">{{.}}</span>{{end}}</div><div>
<label for="phone">Phone (optional)</label><input id="phone" name="phone" value="{{.Form.Student.Phone}}">
{{with index .Form.Errors "phone"}}<span class="err">{{.}}</span>{{end}}</div></div>
<label for="courseCode">Course</label>
<select id="courseCode" name="courseCode"><option value="">Choose a course...</option>
{{range .Courses}}<option value="{{.Code}}" {{if eq .Code $.Form.Student.CourseCode}}selected{{end}}>{{.Code}} - {{.Name}}</option>{{end}}</select>
{{with index .Form.Errors "courseCode"}}<span class="err">{{.}}</span>{{end}}
{{with index .Form.Errors "enrolments"}}<span class="err">{{.}}</span>{{end}}
<p><button>{{if .Form.IsEdit}}Save changes{{else}}Add student{{end}}</button>
<a class="btn" style="background:#6b7490" href="{{if .Form.IsEdit}}/students/{{.Form.Student.ID}}{{else}}/{{end}}">Cancel</a></p>
</form></div>
{{template "foot" .}}{{end}}

{{define "error"}}{{template "head" .}}
<div class="card"><h1>{{.Title}}</h1><p>{{.Error}}</p><p><a class="btn" href="/">Back to home</a></p></div>
{{template "foot" .}}{{end}}
`
