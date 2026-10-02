package httpapi

import (
	"errors"
	"net/http"

	"private/agent_basedon_eino/internal/agent/skills"
)

func (s *Server) registerSkillRoutes() {
	s.mux.HandleFunc("GET /api/skills", s.handleListSkills)
	s.mux.HandleFunc("POST /api/skills", s.handleSaveSkill)
	s.mux.HandleFunc("PUT /api/skills/{name}", s.handleSaveSkillByName)
	s.mux.HandleFunc("DELETE /api/skills/{name}", s.handleDeleteSkill)
	s.mux.HandleFunc("POST /api/skills/reload", s.handleReloadSkills)
}

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Skills.List(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	if list == nil {
		list = []skills.Skill{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"skills": list})
}

func (s *Server) handleSaveSkill(w http.ResponseWriter, r *http.Request) {
	s.saveSkill(w, r, "")
}

func (s *Server) handleSaveSkillByName(w http.ResponseWriter, r *http.Request) {
	name, err := RequirePath(r, "name")
	if err != nil {
		WriteError(w, err)
		return
	}
	s.saveSkill(w, r, name)
}

func (s *Server) saveSkill(w http.ResponseWriter, r *http.Request, name string) {
	var sk skills.Skill
	if err := DecodeJSON(r, &sk); err != nil {
		WriteError(w, err)
		return
	}
	if name != "" {
		sk.Name = name
	}
	// 磁盘来源的技能不接受通过接口改写：那会让页面上的编辑和磁盘上的文件各执一词。
	// 想改就改文件，或者另存为一个同名的数据库技能来覆盖它。
	// Disk-sourced skills are not editable through the API: that would leave the UI and the file
	// on disk disagreeing. Edit the file, or save a database skill under the same name to
	// override it.
	if sk.Source == skills.SourceDisk {
		WriteError(w, &ValidationError{
			Field: "source",
			Reason: "磁盘来源的技能不能通过接口修改，请直接编辑对应的 SKILL.md 文件 / " +
				"disk-sourced skills cannot be edited through the API; edit the SKILL.md file directly",
		})
		return
	}
	saved, err := s.deps.Skills.Save(r.Context(), sk)
	if err != nil {
		WriteError(w, &ValidationError{Reason: err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	name, err := RequirePath(r, "name")
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := s.deps.Skills.Delete(r.Context(), name); err != nil {
		if errors.Is(err, skills.ErrNotFound) {
			WriteError(w, &NotFoundError{Kind: "skill", ID: name})
			return
		}
		WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleReloadSkills 立即作废目录扫描缓存。
// 技能目录有几秒缓存，改完文件想马上看到效果时用它。
// handleReloadSkills invalidates the directory scan cache immediately. The skills directory is
// cached for a few seconds, and this is how to see a file edit take effect right away.
func (s *Server) handleReloadSkills(w http.ResponseWriter, r *http.Request) {
	s.deps.Skills.Reload()
	list, err := s.deps.Skills.List(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"count": len(list)})
}
