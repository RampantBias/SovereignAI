package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Operation string

const (
	Add      Operation = "add"
	Subtract Operation = "subtract"
	Multiply Operation = "multiply"
)

type Equation struct {
	Operation Operation `json:"operation"`
	Left      float64   `json:"left"`
	Right     float64   `json:"right"`
}

type HealthResponse struct {
	Status string `json:"status"`
}

type CalculateResponse struct {
	Result float64 `json:"result"`
}

type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newRouter() *gin.Engine {
	router := gin.New()
	router.GET("/healthz", getHealthz)
	router.POST("/api/v1/calculate", postCalculate)
	return router
}

func main() {
	if err := newRouter().Run(":8080"); err != nil {
		log.Fatal(err)
	}
}

func getHealthz(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, HealthResponse{Status: "ok"})
}

func postCalculate(ctx *gin.Context) {
	var equation Equation

	// Bind values
	if err := ctx.ShouldBindJSON(&equation); err != nil {
		// Bad input
		ctx.JSON(http.StatusBadRequest, ErrorResponse{
			Error: APIError{
				Code:    "invalid_request",
				Message: "request body is invalid",
			},
		})
		return
	}

	var result float64
	switch equation.Operation {
	case Add:
		result = equation.Left + equation.Right
	case Subtract:
		result = equation.Left - equation.Right
	case Multiply:
		result = equation.Left * equation.Right
	default:
		ctx.JSON(http.StatusBadRequest, ErrorResponse{
			Error: APIError{
				Code:    "unsupported_operation",
				Message: "operation is not supported",
			},
		})
		return
	}

	ctx.JSON(http.StatusOK, CalculateResponse{Result: result})
}
