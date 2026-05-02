//go:build ignore

package handlers

import (
	"github.com/pkg/errors"
)

type AuthorService struct {
	db DB
}

func (s *AuthorService) HandleAuthorIndex(req Request) Response {
	rows, err := s.db.Query("SELECT * FROM authors")
	if err != nil {
		return Response{Status: 503, Body: errors.Wrap(err, "query authors").Error()}
	}
	return Response{Status: 200, Body: rows}
}

func (s *AuthorService) HandleAuthorShow(req Request) Response {
	id, ok := req.Param("id").(string)
	if !ok {
		return Response{Status: 422, Body: "id must be string"}
	}
	row, err := s.db.QueryOne("SELECT * FROM authors WHERE id = ?", id)
	if err != nil {
		return Response{Status: 404, Body: errors.Wrap(err, "find author").Error()}
	}
	return Response{Status: 200, Body: row}
}

func (s *AuthorService) HandleAuthorStore(req Request) Response {
	body, err := req.JSON()
	if err != nil {
		return Response{Status: 422, Body: errors.Wrap(err, "parse body").Error()}
	}
	id, err := s.db.Insert("authors", body)
	if err != nil {
		return Response{Status: 503, Body: errors.Wrap(err, "insert author").Error()}
	}
	return Response{Status: 201, Body: map[string]any{"id": id}}
}

func (s *AuthorService) HandleAuthorUpdate(req Request) Response {
	id, _ := req.Param("id").(string)
	body, err := req.JSON()
	if err != nil {
		return Response{Status: 422, Body: errors.Wrap(err, "parse body").Error()}
	}
	if err := s.db.UpdateOne("authors", id, body); err != nil {
		return Response{Status: 503, Body: errors.Wrap(err, "update author").Error()}
	}
	return Response{Status: 204}
}

func (s *AuthorService) HandleAuthorDestroy(req Request) Response {
	id, _ := req.Param("id").(string)
	if err := s.db.DeleteOne("authors", id); err != nil {
		return Response{Status: 503, Body: errors.Wrap(err, "delete author").Error()}
	}
	return Response{Status: 204}
}
