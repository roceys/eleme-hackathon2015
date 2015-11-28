package main

import (
	"./redigo/redis"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	LOGIN                 = "/login"
	QUERY_FOOD            = "/foods"
	CREATE_CART           = "/carts"
	Add_FOOD              = "/carts/"
	SUBMIT_OR_QUERY_ORDER = "/orders"
	QUERY_ALL_ORDERS      = "/admin/orders"
)

const (
	TOTAL_NUM_FIELD = 0
	ROOT_TOKEN      = "1"
)

// tuning parameters
const (
	CACHE_LEN = 73
)

var (
	USER_AUTH_FAIL_MSG       = []byte("{\"code\":\"USER_AUTH_FAIL\",\"message\":\"用户名或密码错误\"}")
	MALFORMED_JSON_MSG       = []byte("{\"code\": \"MALFORMED_JSON\",\"message\": \"格式错误\"}")
	EMPTY_REQUEST_MSG        = []byte("{\"code\": \"EMPTY_REQUEST\",\"message\": \"请求体为空\"}")
	INVALID_ACCESS_TOKEN_MSG = []byte("{\"code\": \"INVALID_ACCESS_TOKEN\",\"message\": \"无效的令牌\"}")
	CART_NOT_FOUND_MSG       = []byte("{\"code\": \"CART_NOT_FOUND\", \"message\": \"篮子不存在\"}")
	NOT_AUTHORIZED_CART_MSG  = []byte("{\"code\": \"NOT_AUTHORIZED_TO_ACCESS_CART\",\"message\": \"无权限访问指定的篮子\"}")
	FOOD_OUT_OF_LIMIT_MSG    = []byte("{\"code\": \"FOOD_OUT_OF_LIMIT\",\"message\": \"篮子中食物数量超过了三个\"}")
	FOOD_NOT_FOUND_MSG       = []byte("{\"code\": \"FOOD_NOT_FOUND\",\"message\": \"食物不存在\"}")
	FOOD_OUT_OF_STOCK_MSG    = []byte("{\"code\": \"FOOD_OUT_OF_STOCK\", \"message\": \"食物库存不足\"}")
	ORDER_OUT_OF_LIMIT_MSG   = []byte("{\"code\": \"ORDER_OUT_OF_LIMIT\",\"message\": \"每个用户只能下一单\"}")
)

var (
	server *http.ServeMux
)

func InitService(addr string) {
	server = http.NewServeMux()
	server.HandleFunc(LOGIN, login)
	server.HandleFunc(QUERY_FOOD, queryFood)
	server.HandleFunc(CREATE_CART, createCart)
	server.HandleFunc(Add_FOOD, addFood)
	server.HandleFunc(SUBMIT_OR_QUERY_ORDER, orderProcess)
	server.HandleFunc(QUERY_ALL_ORDERS, queryAllOrders)
	if err := http.ListenAndServe(addr, server); err != nil {
		fmt.Println(err)
	}
}

func login(writer http.ResponseWriter, req *http.Request) {
	isEmpty, body := checkBodyEmpty(writer, req)
	if isEmpty {
		return
	}
	var user LoginJson
	if err := json.Unmarshal(body, &user); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		writer.Write(MALFORMED_JSON_MSG)
		return
	}
	userIdAndPass, ok := UserMap[user.Username]
	if !ok || userIdAndPass.Password != user.Password {
		writer.WriteHeader(http.StatusForbidden)
		writer.Write(USER_AUTH_FAIL_MSG)
		return
	}
	token := userIdAndPass.Id
	userId, _ := strconv.Atoi(token)
	CacheUserLogin[userId] = -1
	rs := Pool.Get()
	rs.Do("SADD", "tokens", token)
	rs.Close()
	okMsg := []byte("{\"user_id\":" + token + ",\"username\":\"" + user.Username + "\",\"access_token\":\"" + strconv.Itoa(userId-1) + "\"}")
	writer.WriteHeader(http.StatusOK)
	writer.Write(okMsg)
}

func queryFood(writer http.ResponseWriter, req *http.Request) {
	rs := Pool.Get()
	if exist, _ := authorize(writer, req, rs); !exist {
		rs.Close()
		return
	}
	rs.Close()

	writer.WriteHeader(http.StatusOK)
	writer.Write(CacheFoodJson)
}

func createCart(writer http.ResponseWriter, req *http.Request) {
	rs := Pool.Get()
	exist, token := authorize(writer, req, rs)
	if !exist {
		rs.Close()
		return
	}

	cart_id, _ := redis.Int(rs.Do("INCR", "cart_id"))

	if cart_id > CacheCartId {
		CacheCartId = cart_id
	}

	rs.Do("HSET", "cart:"+strconv.Itoa(cart_id)+":"+token, TOTAL_NUM_FIELD, 0)
	rs.Close()

	writer.WriteHeader(http.StatusOK)
	writer.Write([]byte("{\"cart_id\": \"" + strconv.Itoa(cart_id) + "\"}"))
}

func addFood(writer http.ResponseWriter, req *http.Request) {
	// script version
	rs := Pool.Get()
	userExist, token := authorize(writer, req, rs)
	if !userExist {
		rs.Close()
		return
	}
	isEmpty, body := checkBodyEmpty(writer, req)
	if isEmpty {
		rs.Close()
		return
	}

	var item CartItem
	if err := json.Unmarshal(body, &item); err != nil {
		rs.Close()
		writer.WriteHeader(http.StatusBadRequest)
		writer.Write(MALFORMED_JSON_MSG)
		return
	}

	if item.FoodId < 1 || item.FoodId > MaxFoodID {
		rs.Close()
		writer.WriteHeader(http.StatusNotFound)
		writer.Write(FOOD_NOT_FOUND_MSG)
		return
	}

	// transaction problem
	cartIdStr := strings.Split(req.URL.Path, "/")[2]
	cartId, _ := strconv.Atoi(cartIdStr)

	//  STASRT CART_NOT_FOUND_MSG
	if cartId < 1 {
		rs.Close()
		writer.WriteHeader(http.StatusNotFound)
		writer.Write(CART_NOT_FOUND_MSG)
		return
	}

	flag, _ := redis.Int(LuaAddFood.Do(rs, cartId, token, "cart:"+cartIdStr+":"+token, item.FoodId, item.Count))
	rs.Close()
	// if err != nil {
	// 	fmt.Println(err)
	// 	return
	// }

	if flag == 0 {
		// fmt.Printf("Success: CartId, item.FoodId, item.Count = %d, %d, %d\n", cartId, item.FoodId, item.Count)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if flag == 1 {
		writer.WriteHeader(http.StatusNotFound)
		writer.Write(CART_NOT_FOUND_MSG)
		return
	}
	if flag == 2 {
		writer.WriteHeader(http.StatusUnauthorized)
		writer.Write(NOT_AUTHORIZED_CART_MSG)
		return
	}
	if flag == 3 {
		writer.WriteHeader(http.StatusForbidden)
		writer.Write(FOOD_OUT_OF_LIMIT_MSG)
		return
	}
	//script version end

	// rs := Pool.Get()
	// userExist, token := authorize(writer, req, rs)
	// if !userExist {
	// 	rs.Close()
	// 	return
	// }
	// isEmpty, body := checkBodyEmpty(writer, req)
	// if isEmpty {
	// 	rs.Close()
	// 	return
	// }
	// // transaction problem
	// cartIdStr := strings.Split(req.URL.Path, "/")[2]
	// cartId, _ := strconv.Atoi(cartIdStr)

	// //  STASRT CART_NOT_FOUND_MSG
	// if cartId < 1 {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusNotFound)
	// 	writer.Write(CART_NOT_FOUND_MSG)
	// 	return
	// }
	// if cartId > CacheCartId {
	// 	cartIdMax, err := redis.Int(rs.Do("GET", "cart_id"))
	// 	if err != nil || cartId > cartIdMax {
	// 		rs.Close()
	// 		writer.WriteHeader(http.StatusNotFound)
	// 		writer.Write(CART_NOT_FOUND_MSG)
	// 		return
	// 	}
	// 	CacheCartId = cartIdMax
	// }
	// // END

	// cartKey := "cart:" + cartIdStr + ":" + string(token)
	// total, cartExistErr := redis.Int(rs.Do("HGET", cartKey, TOTAL_NUM_FIELD))
	// if cartExistErr != nil {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusUnauthorized)
	// 	writer.Write(NOT_AUTHORIZED_CART_MSG)
	// 	return
	// }

	// // TODO Trick: the request count is more than 0? Yes, we can checkout whether
	// // total is more than 3 advanced.
	// var item CartItem
	// if err := json.Unmarshal(body, &item); err != nil {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusBadRequest)
	// 	writer.Write(MALFORMED_JSON_MSG)
	// 	return
	// }
	// total += item.Count
	// if total > 3 {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusForbidden)
	// 	writer.Write(FOOD_OUT_OF_LIMIT_MSG)
	// 	return
	// }

	// // rapid test
	// if item.FoodId < 1 || item.FoodId > MaxFoodID {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusNotFound)
	// 	writer.Write(FOOD_NOT_FOUND_MSG)
	// 	return
	// }

	// if tag, _ := redis.Bool(rs.Do("HEXISTS", "orders", token)); tag {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusNoContent)
	// 	return
	// }

	// foodCountInCart, foodErr := redis.Int(rs.Do("HGET", cartKey, item.FoodId))
	// //fmt.Println("cartKey = ", cartKey, "item.FoodId = ", item.FoodId, "item.Count = ", item.Count, "foodCountInCart = ", foodCountInCart)
	// if foodErr != nil {
	// 	rs.Do("HMSET", cartKey, TOTAL_NUM_FIELD, total, item.FoodId, item.Count)
	// } else {
	// 	// if item.Count+foodCount < 0, how to do?
	// 	rs.Do("HMSET", cartKey, TOTAL_NUM_FIELD, total, item.FoodId, item.Count+foodCountInCart)
	// }

	// rs.Close()
	// writer.WriteHeader(http.StatusNoContent)
	// return

}

func orderProcess(writer http.ResponseWriter, req *http.Request) {
	if req.Method == "POST" {
		submitOrder(writer, req)
	} else {
		queryOneOrder(writer, req)
	}
}

func submitOrder(writer http.ResponseWriter, req *http.Request) {
	//script version
	rs := Pool.Get()
	userExist, token := authorize(writer, req, rs)
	if !userExist {
		rs.Close()
		return
	}
	isEmpty, body := checkBodyEmpty(writer, req)
	if isEmpty {
		rs.Close()
		return
	}
	var cartIdJson CartIdJson
	if err := json.Unmarshal(body, &cartIdJson); err != nil {
		rs.Close()
		writer.WriteHeader(http.StatusBadRequest)
		writer.Write(MALFORMED_JSON_MSG)
		return
	}
	cartIdStr := cartIdJson.CartId

	//fmt.Printf("submitOrder: token=%s, cartId=%s\n", token, cartIdJson.CartId)

	// transaction problem

	cartId, _ := strconv.Atoi(cartIdStr)

	// copy from the same code above
	//  STASRT CART_NOT_FOUND_MSG
	if cartId < 1 {
		rs.Close()
		writer.WriteHeader(http.StatusNotFound)
		writer.Write(CART_NOT_FOUND_MSG)
		return
	}

	// var flag int
	// if cartId > CacheCartId {
	// 	flags, err := redis.Ints(LuaSubmitOrder.Do(rs, cartIdStr, token))
	// 	if err != nil {
	// 		fmt.Println(err)
	// 	}
	// 	flag = flags[0]
	// 	CacheCartId = flags[1]
	// } else {
	// 	flag, _ = redis.Int(LuaSubmitOrderWithoutCartId.Do(rs, cartIdStr, token))
	// }

	flag, _ := redis.Int(LuaSubmitOrder.Do(rs, cartIdStr, token, "cart:"+cartIdStr+":"+token))
	rs.Close()

	if flag == 0 {
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte("{\"id\": \"" + token + "\"}"))
		return
	}
	if flag == 1 {
		writer.WriteHeader(http.StatusNotFound)
		writer.Write(CART_NOT_FOUND_MSG)
		return
	}
	if flag == 2 {
		writer.WriteHeader(http.StatusUnauthorized)
		writer.Write(NOT_AUTHORIZED_CART_MSG)
		return
	}
	if flag == 3 {
		writer.WriteHeader(http.StatusForbidden)
		writer.Write(FOOD_OUT_OF_STOCK_MSG)
		return
	}
	if flag == 4 {
		writer.WriteHeader(http.StatusForbidden)
		writer.Write(ORDER_OUT_OF_LIMIT_MSG)
		return
	}
	//script version end

	// rs := Pool.Get()
	// userExist, token := authorize(writer, req, rs)
	// if !userExist {
	// 	rs.Close()
	// 	return
	// }
	// isEmpty, body := checkBodyEmpty(writer, req)
	// if isEmpty {
	// 	rs.Close()
	// 	return
	// }
	// var cartIdJson CartIdJson
	// if err := json.Unmarshal(body, &cartIdJson); err != nil {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusBadRequest)
	// 	writer.Write(MALFORMED_JSON_MSG)
	// 	return
	// }
	// cartIdStr := cartIdJson.CartId

	// //fmt.Printf("submitOrder: token=%s, cartId=%s\n", token, cartIdJson.CartId)

	// // transaction problem

	// cartId, _ := strconv.Atoi(cartIdStr)

	// // copy from the same code above
	// //  STASRT CART_NOT_FOUND_MSG
	// if cartId < 1 {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusNotFound)
	// 	writer.Write(CART_NOT_FOUND_MSG)
	// 	return
	// }
	// if cartId > CacheCartId {
	// 	cartIdMax, err := redis.Int(rs.Do("GET", "cart_id"))
	// 	if err != nil || cartId > cartIdMax {
	// 		rs.Close()
	// 		writer.WriteHeader(http.StatusNotFound)
	// 		writer.Write(CART_NOT_FOUND_MSG)
	// 		return
	// 	}
	// 	CacheCartId = cartIdMax
	// }
	// // END

	// cartKey := "cart:" + cartIdStr + ":" + token
	// _, cartExistErr := redis.Int(rs.Do("HGET", cartKey, TOTAL_NUM_FIELD))
	// if cartExistErr != nil {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusUnauthorized)
	// 	writer.Write(NOT_AUTHORIZED_CART_MSG)
	// 	//fmt.Println(string(NOT_AUTHORIZED_CART_MSG))
	// 	return
	// }

	// // transaction problem
	// foodIdAndCounts, _ := redis.Ints(rs.Do("HGETALL", cartKey))
	// var cart Cart
	// itemNum := len(foodIdAndCounts)/2 - 1
	// //fmt.Println("itemNum =", itemNum)
	// if itemNum == 0 {
	// 	cart.Items = []CartItem{}
	// } else {
	// 	cart.Items = make([]CartItem, itemNum)
	// 	cnt := 0
	// 	for i := 0; i < len(foodIdAndCounts); i += 2 {
	// 		if foodIdAndCounts[i] != TOTAL_NUM_FIELD {
	// 			cart.Items[cnt].FoodId = foodIdAndCounts[i]
	// 			cart.Items[cnt].Count = foodIdAndCounts[i+1]
	// 			cnt++
	// 			//fmt.Println("foodId, reqCount =", foodIdAndCounts[i], foodIdAndCounts[i+1])
	// 		}
	// 	}
	// }
	// for i := 0; i < len(cart.Items); i++ {
	// 	stock, _ := redis.Int(rs.Do("HGET", "food:"+strconv.Itoa(cart.Items[i].FoodId), "stock"))
	// 	tmp := stock - cart.Items[i].Count
	// 	cart.Items[i].Count = tmp
	// 	//fmt.Println("stock, reqCount = ", stock, cart.Items[i].Count)
	// 	if tmp < 0 {
	// 		rs.Close()
	// 		writer.WriteHeader(http.StatusForbidden)
	// 		writer.Write(FOOD_OUT_OF_STOCK_MSG)
	// 		//fmt.Println(string(FOOD_OUT_OF_STOCK_MSG))
	// 		return
	// 	}
	// }

	// // no transaction problem

	// if tag, _ := redis.Bool(rs.Do("HEXISTS", "orders", token)); tag {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusForbidden)
	// 	writer.Write(ORDER_OUT_OF_LIMIT_MSG)
	// 	return
	// }

	// isSuccess, _ := redis.Int(rs.Do("HSETNX", "orders", token, cartIdStr))
	// //fmt.Println("SETNX", "order:"+token, cartIdStr+":"+token)
	// //fmt.Println("isSuccess =", isSuccess)
	// if isSuccess == 0 {
	// 	rs.Close()
	// 	writer.WriteHeader(http.StatusForbidden)
	// 	writer.Write(ORDER_OUT_OF_LIMIT_MSG)
	// 	//fmt.Println(string(ORDER_OUT_OF_LIMIT_MSG))
	// 	return
	// }

	// for i := 0; i < len(cart.Items); i++ {
	// 	rs.Do("HSET", "food:"+strconv.Itoa(cart.Items[i].FoodId), "stock", cart.Items[i].Count)
	// 	//fmt.Println("food:"+strconv.Itoa(cart.Items[i].FoodId), "stock", cart.Items[i].Count)
	// }
	// rs.Close()
	// writer.WriteHeader(http.StatusOK)
	// writer.Write([]byte("{\"id\": \"" + token + "\"}"))
	// //fmt.Println("order success")
	// return

}

func queryOneOrder(writer http.ResponseWriter, req *http.Request) {
	rs := Pool.Get()
	exist, token := authorize(writer, req, rs)
	if !exist {
		rs.Close()
		return
	}

	cartId, err := redis.String(rs.Do("HGET", "orders", token))
	if err != nil {
		rs.Close()
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte("[]"))
		return
	}

	foodIdAndCounts, _ := redis.Ints(rs.Do("HGETALL", "cart:"+cartId+":"+token))
	rs.Close()

	var carts [1]Cart
	cart := &carts[0]
	itemNum := len(foodIdAndCounts)/2 - 1
	cart.Id = token
	if itemNum == 0 {
		cart.Items = []CartItem{}
	} else {
		cart.Items = make([]CartItem, itemNum)
		cnt := 0
		for i := 0; i < len(foodIdAndCounts); i += 2 {
			if foodIdAndCounts[i] != 0 {
				fid := foodIdAndCounts[i]
				quantity := foodIdAndCounts[i+1]
				cart.Items[cnt].FoodId = fid
				cart.Items[cnt].Count = quantity
				//fmt.Println("FoodList[fid]", fid, FoodList[fid])
				cart.TotalPrice += quantity * FoodList[fid].Price
				cnt++
			}
		}
	}

	body, _ := json.Marshal(carts)
	// fmt.Println(string(body))
	writer.WriteHeader(http.StatusOK)
	writer.Write(body)
}

func queryAllOrders(writer http.ResponseWriter, req *http.Request) {
	start := time.Now()
	rs := Pool.Get()
	exist, token := authorize(writer, req, rs)
	if !exist {
		rs.Close()
		return
	}

	if token != ROOT_TOKEN {
		rs.Close()
		writer.WriteHeader(http.StatusUnauthorized)
		writer.Write(INVALID_ACCESS_TOKEN_MSG)
		return
	}

	tot, _ := redis.Int(rs.Do("HLEN", "orders"))
	carts := make([]CartDetail, tot)
	cartidAndTokens := make([]int, tot*2)
	cartidAndTokens, _ = redis.Ints(rs.Do("HGETALL", "orders"))
	cnt := 0

	for i := 0; i < tot*2; i += 2 {
		token := cartidAndTokens[i]
		carId := cartidAndTokens[i+1]
		rs.Send("HGETALL", "cart:"+strconv.Itoa(carId)+":"+strconv.Itoa(token))
	}
	rs.Flush()

	for i := 0; i < tot*2; i += 2 {

		token := cartidAndTokens[i]
		// carId := cartidAndTokens[i+1]
		// foodIdAndCounts, _ := redis.Ints(rs.Do("HGETALL", "cart:"+strconv.Itoa(carId)+":"+strconv.Itoa(token)))
		foodIdAndCounts, _ := redis.Ints(rs.Receive())

		itemNum := len(foodIdAndCounts)/2 - 1
		carts[cnt].Id = strconv.Itoa(token)
		carts[cnt].UserId = token
		if itemNum == 0 {
			carts[cnt].Items = []CartItem{}
		} else {
			carts[cnt].Items = make([]CartItem, itemNum)
			count := 0
			for j := 0; j < len(foodIdAndCounts); j += 2 {
				if foodIdAndCounts[j] != 0 {
					fid := foodIdAndCounts[j]
					carts[cnt].Items[count].FoodId = fid
					carts[cnt].Items[count].Count = foodIdAndCounts[j+1]
					carts[cnt].TotalPrice += FoodList[fid].Price * foodIdAndCounts[j+1]
					count++
				}
			}
			cnt++
		}
	}

	rs.Close()
	body, _ := json.Marshal(carts)
	writer.WriteHeader(http.StatusOK)
	writer.Write(body)
	end := time.Now().Sub(start)
	fmt.Println("queryAllOrders time: ", end.String())
}

// every action will do authorization except logining
// return the flag that indicate whether is authroized or not
func authorize(writer http.ResponseWriter, req *http.Request, rs redis.Conn) (bool, string) {
	req.ParseForm()
	token := req.Form.Get("access_token")
	if token == "" {
		token = req.Header.Get("Access-Token")
	}

	userId, err := strconv.Atoi(token)
	if err != nil {
		writer.WriteHeader(http.StatusUnauthorized)
		writer.Write(INVALID_ACCESS_TOKEN_MSG)
		return false, ""
	}
	userId += 1
	token = strconv.Itoa(userId)

	if userId < 1 || userId > MaxUserID {
		writer.WriteHeader(http.StatusUnauthorized)
		writer.Write(INVALID_ACCESS_TOKEN_MSG)
		return false, ""
	}

	if CacheUserLogin[userId] == -1 {
		return true, token
	}

	if exist, _ := redis.Bool(rs.Do("SISMEMBER", "tokens", token)); !exist {
		writer.WriteHeader(http.StatusUnauthorized)
		writer.Write(INVALID_ACCESS_TOKEN_MSG)
		return false, ""
	}

	CacheUserLogin[userId] = -1

	return true, token
}

func checkBodyEmpty(writer http.ResponseWriter, req *http.Request) (bool, []byte) {
	tmp := make([]byte, CACHE_LEN)
	if n, _ := req.Body.Read(tmp); n == 0 {
		writer.WriteHeader(http.StatusBadRequest)
		writer.Write(EMPTY_REQUEST_MSG)
		return true, nil
	} else {
		return false, tmp[:n]
	}
}
