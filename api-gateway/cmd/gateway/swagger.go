package main

// @title           Mallow API Gateway
// @version         1.0
// @description     Gateway for Mallow algorithmic trading system.
// @description
// @description     All protected routes require a Bearer JWT token obtained from the Identity service (`/api/v1/auth/login`).
//
// @schemes         http https
// @BasePath        /
//
// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            Authorization
// @description     JWT token — prefix with "Bearer "
