# Glossary

## Colliding terms

- None yet. "habit" (the thing you track) and "habit log" (one day's record of it) are kept apart: code and API use `habit` only for the name, and `HabitLog` / `habit_logs` for records.

## Terms

| Term | Meaning | Where it lives |
|---|---|---|
| entry | The diary text for one date. One per date; writing again replaces it | table `entries`, type `Entry`, `/entries/{date}` |
| habit | A name for something tracked daily (`walk`, `stretch`, `hatebu`). Not stored on its own yet | path segment `{habit}`, column `habit_logs.habit` |
| habit log | The record that a habit happened on a date, with an optional number (steps, minutes) | table `habit_logs`, type `HabitLog`, `/habits/{date}/{habit}` |
| value | The optional number on a habit log. `null` means a plain done check | column `habit_logs.value` |
| date | A calendar day as `YYYY-MM-DD`, in the writer's local time | primary keys of both tables |
